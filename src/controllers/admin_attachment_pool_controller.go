package controllers

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"gorm.io/gorm"

	"a1ctf/src/db/models"
	"a1ctf/src/tasks"
	attachmentpool "a1ctf/src/utils/attachment_pool"
	dbtool "a1ctf/src/utils/db_tool"
	i18ntool "a1ctf/src/utils/i18n_tool"
	"a1ctf/src/utils/ristretto_tool"
)

const (
	poolMaxFileSize    = 50 * 1024 * 1024  // 单个池文件上限
	poolMaxTotalSize   = 500 * 1024 * 1024 // 单次导入解压后总大小上限
	poolMaxFileCount   = 1000              // 单次导入文件数上限
	poolManifestName   = "manifest.json"
	poolUploadDir      = "./data/uploads/files"
	poolStoredBaseName = "attachment"
)

var errPoolImportTooLarge = errors.New("AttachmentPoolImportTooLarge")

// AttachmentPoolImportEntry 导入结果，file_name 仅用于管理端展示原始文件名
type AttachmentPoolImportEntry struct {
	FileID   string `json:"file_id"`
	Flag     string `json:"flag"`
	FileName string `json:"file_name"`
}

// AttachmentPoolAssignment 管理端查看的分配结果
type AttachmentPoolAssignment struct {
	GameID     int64   `json:"game_id"`
	GameName   string  `json:"game_name"`
	TeamID     int64   `json:"team_id"`
	TeamName   string  `json:"team_name"`
	Flag       string  `json:"flag"`
	EntryIndex int     `json:"entry_index"` // -1 表示条目已被删除
	FileID     *string `json:"file_id"`
	Solved     bool    `json:"solved"`
}

func parseChallengeIDParam(c *gin.Context) (int64, bool) {
	challengeID, err := strconv.ParseInt(c.Param("challenge_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "InvalidChallengeID"}),
		})
		return 0, false
	}
	return challengeID, true
}

func loadPoolChallenge(c *gin.Context, challengeID int64) (*models.Challenge, *models.AttachmentConfig, bool) {
	var challenge models.Challenge
	if err := dbtool.DB().Where("challenge_id = ?", challengeID).First(&challenge).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"code":    404,
				"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "ChallengeNotFound"}),
			})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    500,
				"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "SystemError"}),
			})
		}
		return nil, nil, false
	}

	pool := attachmentpool.FindPool(challenge.Attachments)
	if challenge.FlagType != models.FlagTypeAttachmentPool || pool == nil || len(pool.Pool) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "ChallengeNotAttachmentPool"}),
		})
		return nil, nil, false
	}
	return &challenge, pool, true
}

// AdminReassignAttachmentPool 按当前附件池为所有未解出的队伍重新分配（原地更新 team_flags）
func AdminReassignAttachmentPool(c *gin.Context) {
	challengeID, ok := parseChallengeIDParam(c)
	if !ok {
		return
	}
	_, pool, ok := loadPoolChallenge(c, challengeID)
	if !ok {
		return
	}

	var changed int
	var gameIDs []int64
	err := dbtool.DB().Transaction(func(tx *gorm.DB) error {
		var err error
		changed, gameIDs, err = attachmentpool.ReassignUnsolved(tx, challengeID, pool.Pool)
		return err
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "SystemError"}),
		})
		return
	}

	for _, gameID := range gameIDs {
		ristretto_tool.InvalidateTeamFlags(gameID, challengeID)
	}

	challengeIDStr := strconv.FormatInt(challengeID, 10)
	tasks.LogAdminOperation(c, models.ActionUpdate, models.ResourceTypeChallenge, &challengeIDStr, map[string]interface{}{
		"operation":     "reassign_attachment_pool",
		"changed_teams": changed,
	})

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": gin.H{
			"changed_teams": changed,
		},
	})
}

// AdminGetAttachmentPoolAssignments 查看每个队伍分配到的附件池条目（所有比赛）
func AdminGetAttachmentPoolAssignments(c *gin.Context) {
	challengeID, ok := parseChallengeIDParam(c)
	if !ok {
		return
	}
	_, pool, ok := loadPoolChallenge(c, challengeID)
	if !ok {
		return
	}

	type row struct {
		GameID      int64
		GameName    string
		TeamID      int64
		TeamName    string
		FlagContent string
		Solved      bool
	}
	var rows []row
	if err := dbtool.DB().Model(&models.TeamFlag{}).
		Select(`team_flags.game_id, games.name AS game_name, team_flags.team_id, teams.team_name, team_flags.flag_content,
			EXISTS (SELECT 1 FROM solves s WHERE s.game_id = team_flags.game_id AND s.challenge_id = team_flags.challenge_id AND s.team_id = team_flags.team_id AND s.solve_status = ?) AS solved`, models.SolveCorrect).
		Joins("JOIN teams ON teams.team_id = team_flags.team_id").
		Joins("JOIN games ON games.game_id = team_flags.game_id").
		Where("team_flags.challenge_id = ?", challengeID).
		Order("team_flags.game_id, team_flags.team_id").
		Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "SystemError"}),
		})
		return
	}

	result := make([]AttachmentPoolAssignment, 0, len(rows))
	for _, r := range rows {
		idx := attachmentpool.EntryIndexForFlag(pool.Pool, r.FlagContent)
		item := AttachmentPoolAssignment{
			GameID:     r.GameID,
			GameName:   r.GameName,
			TeamID:     r.TeamID,
			TeamName:   r.TeamName,
			Flag:       r.FlagContent,
			EntryIndex: idx,
			Solved:     r.Solved,
		}
		if idx >= 0 {
			fileID := pool.Pool[idx].FileID
			item.FileID = &fileID
		}
		result = append(result, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": result,
	})
}

type poolImportFile struct {
	name string // 原始文件名（不含目录）
	data []byte
}

// AdminImportAttachmentPool 导入附件池文件。
//   - 字段 archive：一个 zip，解压后每个文件是一个条目；zip 根目录的 manifest.json（{"文件名": "flag"}）可指定 flag
//   - 字段 files：多个原始文件，每个文件是一个条目
//
// 没有 manifest 指定时，flag 取文件名去掉扩展名。文件以中性文件名保存，避免下载响应头泄露 flag。
// 仅保存文件并返回条目，题目配置由管理员在表单中确认后保存。
func AdminImportAttachmentPool(c *gin.Context) {
	user := c.MustGet("user").(models.User)

	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "NoFileUploaded"}),
		})
		return
	}

	files := make([]poolImportFile, 0)
	manifest := map[string]string{}
	totalSize := int64(0)

	for _, fh := range form.File["archive"] {
		archiveFiles, archiveManifest, err := readPoolArchive(fh)
		if err != nil {
			messageID := "AttachmentPoolInvalidArchive"
			if errors.Is(err, errPoolImportTooLarge) {
				messageID = errPoolImportTooLarge.Error()
			}
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    400,
				"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: messageID}),
			})
			return
		}
		for k, v := range archiveManifest {
			manifest[k] = v
		}
		for _, f := range archiveFiles {
			totalSize += int64(len(f.data))
		}
		files = append(files, archiveFiles...)
	}

	for _, fh := range form.File["files"] {
		if fh.Size > poolMaxFileSize {
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    400,
				"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "FileTooLarge"}),
			})
			return
		}
		data, err := readMultipartFile(fh, poolMaxFileSize)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    400,
				"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "NoFileUploaded"}),
			})
			return
		}
		totalSize += int64(len(data))
		files = append(files, poolImportFile{name: filepath.Base(fh.Filename), data: data})
	}

	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "NoFileUploaded"}),
		})
		return
	}
	if len(files) > poolMaxFileCount || totalSize > poolMaxTotalSize {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: errPoolImportTooLarge.Error()}),
		})
		return
	}

	if err := os.MkdirAll(poolUploadDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "FailedToCreateUploadDirectory"}),
		})
		return
	}

	entries := make([]AttachmentPoolImportEntry, 0, len(files))
	uploads := make([]models.Upload, 0, len(files))
	savedPaths := make([]string, 0, len(files))
	cleanup := func() {
		for _, p := range savedPaths {
			os.Remove(p)
		}
	}

	for _, f := range files {
		flag, ok := manifest[f.name]
		if !ok {
			flag = strings.TrimSuffix(f.name, filepath.Ext(f.name))
		}
		flag = strings.TrimSpace(flag)
		if flag == "" {
			cleanup()
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    400,
				"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "AttachmentPoolInvalidEntry"}),
			})
			return
		}

		fileID := uuid.NewString()
		savedPath := filepath.Join(poolUploadDir, fileID)
		if err := os.WriteFile(savedPath, f.data, 0644); err != nil {
			cleanup()
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    500,
				"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "FailedToSaveFile"}),
			})
			return
		}
		savedPaths = append(savedPaths, savedPath)

		uploads = append(uploads, models.Upload{
			FileID:     fileID,
			UserID:     user.UserID,
			FileName:   poolStoredBaseName + filepath.Ext(f.name),
			FilePath:   savedPath,
			FileType:   "application/octet-stream",
			FileSize:   int64(len(f.data)),
			UploadTime: time.Now().UTC(),
		})
		entries = append(entries, AttachmentPoolImportEntry{FileID: fileID, Flag: flag, FileName: f.name})
	}

	if err := dbtool.DB().Create(&uploads).Error; err != nil {
		cleanup()
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": i18ntool.Translate(c, &i18n.LocalizeConfig{MessageID: "FailedToSaveFileRecord"}),
		})
		return
	}

	tasks.LogAdminOperation(c, models.ActionUpload, models.ResourceTypeFile, nil, map[string]interface{}{
		"operation":  "import_attachment_pool",
		"file_count": len(entries),
	})

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": entries,
	})
}

func readMultipartFile(fh *multipart.FileHeader, limit int64) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// readPoolArchive 解压 zip。只取文件名（忽略目录，防止 zip slip），并限制单文件大小、总大小和文件数（防止 zip 炸弹）。
func readPoolArchive(fh *multipart.FileHeader) ([]poolImportFile, map[string]string, error) {
	raw, err := readMultipartFile(fh, poolMaxTotalSize)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > poolMaxTotalSize {
		return nil, nil, errPoolImportTooLarge
	}

	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, nil, err
	}

	files := make([]poolImportFile, 0)
	manifest := map[string]string{}
	seen := map[string]struct{}{}
	total := int64(0)

	for _, zf := range reader.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		name := path.Base(strings.ReplaceAll(zf.Name, "\\", "/"))
		// 忽略隐藏文件和 macOS 元数据
		if name == "" || name == "." || strings.HasPrefix(name, ".") || strings.HasPrefix(zf.Name, "__MACOSX/") {
			continue
		}
		if len(files) >= poolMaxFileCount {
			return nil, nil, errPoolImportTooLarge
		}
		if zf.UncompressedSize64 > poolMaxFileSize {
			return nil, nil, errPoolImportTooLarge
		}

		rc, err := zf.Open()
		if err != nil {
			return nil, nil, err
		}
		// 不信任头部中的大小，实际读取时再限制
		data, err := io.ReadAll(io.LimitReader(rc, poolMaxFileSize+1))
		rc.Close()
		if err != nil {
			return nil, nil, err
		}
		if len(data) > poolMaxFileSize {
			return nil, nil, errPoolImportTooLarge
		}
		total += int64(len(data))
		if total > poolMaxTotalSize {
			return nil, nil, errPoolImportTooLarge
		}

		if name == poolManifestName {
			if err := sonic.Unmarshal(data, &manifest); err != nil {
				return nil, nil, fmt.Errorf("invalid manifest: %w", err)
			}
			continue
		}

		if _, dup := seen[name]; dup {
			return nil, nil, fmt.Errorf("duplicate file name in archive: %s", name)
		}
		seen[name] = struct{}{}
		files = append(files, poolImportFile{name: name, data: data})
	}

	return files, manifest, nil
}
