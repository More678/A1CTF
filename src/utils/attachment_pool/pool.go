// Package attachmentpool 实现「动态Flag（附件池）」：出题人预先上传 N 个文件，每个文件对应一个 flag，
// 按 hash(team_hash + challenge_id) % N 给队伍分配一个条目。分配结果写入 team_flags 后固定，
// 之后通过「队伍 flag -> 池中条目」反查文件。
package attachmentpool

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"a1ctf/src/db/models"

	"gorm.io/gorm"
)

// 校验失败时返回的错误，Error() 为 i18n MessageID
var (
	ErrPoolFlagTypeMismatch = errors.New("AttachmentPoolFlagTypeMismatch")
	ErrPoolMustBeOnly       = errors.New("AttachmentPoolMustBeOnlyAttachment")
	ErrPoolEmpty            = errors.New("AttachmentPoolEmpty")
	ErrPoolInvalidEntry     = errors.New("AttachmentPoolInvalidEntry")
)

// FindPool 返回题目的附件池配置，不存在时返回 nil
func FindPool(attachments models.AttachmentConfigs) *models.AttachmentConfig {
	for i := range attachments {
		if attachments[i].AttachType == models.AttachmentTypeAttachmentPool {
			return &attachments[i]
		}
	}
	return nil
}

// Validate 校验题目的附件池配置：
//   - flag_type 为附件池 当且仅当 附件中存在附件池
//   - 附件池题只能有这一个附件
//   - 池非空，每个条目都有 file_id 和 flag
func Validate(challenge *models.Challenge) error {
	poolCount := 0
	for _, a := range challenge.Attachments {
		if a.AttachType == models.AttachmentTypeAttachmentPool {
			poolCount++
		}
	}

	isPoolFlag := challenge.FlagType == models.FlagTypeAttachmentPool
	if isPoolFlag != (poolCount > 0) {
		return ErrPoolFlagTypeMismatch
	}
	if !isPoolFlag {
		return nil
	}
	if poolCount != 1 || len(challenge.Attachments) != 1 {
		return ErrPoolMustBeOnly
	}

	pool := challenge.Attachments[0].Pool
	if len(pool) == 0 {
		return ErrPoolEmpty
	}
	for _, entry := range pool {
		if strings.TrimSpace(entry.FileID) == "" || strings.TrimSpace(entry.Flag) == "" {
			return ErrPoolInvalidEntry
		}
	}
	return nil
}

// Index 计算队伍在池中分配到的序号
func Index(teamHash string, challengeID int64, n int) int {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", teamHash, challengeID)))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(n))
}

// EntryForFlag 用队伍的 flag 反查池中条目，找不到返回 nil（条目已被删除）
func EntryForFlag(pool []models.AttachmentPoolEntry, flag string) *models.AttachmentPoolEntry {
	for i := range pool {
		if pool[i].Flag == flag {
			return &pool[i]
		}
	}
	return nil
}

// EntryIndexForFlag 同 EntryForFlag，返回序号，找不到返回 -1
func EntryIndexForFlag(pool []models.AttachmentPoolEntry, flag string) int {
	for i := range pool {
		if pool[i].Flag == flag {
			return i
		}
	}
	return -1
}

// EnsureTeamFlag 确保队伍在该比赛该题目下有附件池 flag，没有则同步分配并写入 team_flags。
// 使用事务级 advisory lock 防止并发请求重复插入。
func EnsureTeamFlag(db *gorm.DB, gameID, challengeID, teamID int64, teamHash string, pool []models.AttachmentPoolEntry) (*models.TeamFlag, error) {
	if len(pool) == 0 {
		return nil, ErrPoolEmpty
	}

	var result models.TeamFlag
	err := db.Transaction(func(tx *gorm.DB) error {
		lockKey := fmt.Sprintf("team_flag:%d:%d:%d", gameID, challengeID, teamID)
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", lockKey).Error; err != nil {
			return err
		}

		err := tx.Where("team_id = ? AND game_id = ? AND challenge_id = ?", teamID, gameID, challengeID).First(&result).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		result = models.TeamFlag{
			GameID:      gameID,
			ChallengeID: challengeID,
			TeamID:      teamID,
			FlagContent: pool[Index(teamHash, challengeID, len(pool))].Flag,
		}
		return tx.Create(&result).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// RemovedFlags 返回旧池中存在、但新池中已不存在的 flag（即被删除或修改的条目）
func RemovedFlags(oldPool, newPool []models.AttachmentPoolEntry) []string {
	newFlags := make(map[string]struct{}, len(newPool))
	for _, e := range newPool {
		newFlags[e.Flag] = struct{}{}
	}
	removed := make([]string, 0)
	for _, e := range oldPool {
		if _, ok := newFlags[e.Flag]; !ok {
			removed = append(removed, e.Flag)
		}
	}
	return removed
}

// unsolvedTeamFlags 查询该题目（所有比赛）下尚未解出的队伍的 team_flags
func unsolvedTeamFlags(tx *gorm.DB, challengeID int64) *gorm.DB {
	return tx.Model(&models.TeamFlag{}).
		Where("team_flags.challenge_id = ?", challengeID).
		Where("NOT EXISTS (SELECT 1 FROM solves s WHERE s.game_id = team_flags.game_id AND s.challenge_id = team_flags.challenge_id AND s.team_id = team_flags.team_id AND s.solve_status = ?)", models.SolveCorrect)
}

// CountAffectedTeams 统计分配到指定 flag 的队伍数（所有比赛，含已解出）
func CountAffectedTeams(tx *gorm.DB, challengeID int64, flags []string) (int64, error) {
	if len(flags) == 0 {
		return 0, nil
	}
	var count int64
	err := tx.Model(&models.TeamFlag{}).Where("challenge_id = ? AND flag_content IN ?", challengeID, flags).Count(&count).Error
	return count, err
}

// ReassignUnsolved 按当前池为所有未解出的队伍重新计算分配，原地 UPDATE flag_content（flag_id 不变，
// 保留关联的提交与作弊记录）。返回发生变化的队伍数和涉及的比赛 ID。
func ReassignUnsolved(tx *gorm.DB, challengeID int64, pool []models.AttachmentPoolEntry) (int, []int64, error) {
	if len(pool) == 0 {
		return 0, nil, ErrPoolEmpty
	}

	type row struct {
		FlagID      int64
		GameID      int64
		FlagContent string
		TeamHash    string
	}
	var rows []row
	if err := unsolvedTeamFlags(tx, challengeID).
		Select("team_flags.flag_id, team_flags.game_id, team_flags.flag_content, teams.team_hash").
		Joins("JOIN teams ON teams.team_id = team_flags.team_id").
		Scan(&rows).Error; err != nil {
		return 0, nil, err
	}

	changed := 0
	gameSet := make(map[int64]struct{})
	for _, r := range rows {
		gameSet[r.GameID] = struct{}{}
		newFlag := pool[Index(r.TeamHash, challengeID, len(pool))].Flag
		if newFlag == r.FlagContent {
			continue
		}
		if err := tx.Model(&models.TeamFlag{}).Where("flag_id = ?", r.FlagID).Update("flag_content", newFlag).Error; err != nil {
			return 0, nil, err
		}
		changed++
	}

	gameIDs := make([]int64, 0, len(gameSet))
	for id := range gameSet {
		gameIDs = append(gameIDs, id)
	}
	return changed, gameIDs, nil
}
