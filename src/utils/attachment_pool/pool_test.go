package attachmentpool

import (
	"fmt"
	"testing"

	"a1ctf/src/db/models"
)

func poolOf(flags ...string) []models.AttachmentPoolEntry {
	pool := make([]models.AttachmentPoolEntry, 0, len(flags))
	for i, f := range flags {
		pool = append(pool, models.AttachmentPoolEntry{FileID: fmt.Sprintf("file-%d", i), Flag: f})
	}
	return pool
}

func poolChallenge(attachments ...models.AttachmentConfig) *models.Challenge {
	return &models.Challenge{FlagType: models.FlagTypeAttachmentPool, Attachments: attachments}
}

func TestIndexIsStableAndInRange(t *testing.T) {
	for n := 1; n <= 7; n++ {
		for team := 0; team < 50; team++ {
			hash := fmt.Sprintf("team-%d", team)
			a := Index(hash, 42, n)
			if a < 0 || a >= n {
				t.Fatalf("index %d out of range for n=%d", a, n)
			}
			if b := Index(hash, 42, n); a != b {
				t.Fatalf("index not stable: %d vs %d", a, b)
			}
		}
	}
}

func TestIndexDependsOnChallenge(t *testing.T) {
	differs := false
	for team := 0; team < 20 && !differs; team++ {
		hash := fmt.Sprintf("team-%d", team)
		differs = Index(hash, 1, 10) != Index(hash, 2, 10)
	}
	if !differs {
		t.Fatal("expected challenge id to affect assignment")
	}
}

func TestValidate(t *testing.T) {
	pool := models.AttachmentConfig{AttachName: "chall.zip", AttachType: models.AttachmentTypeAttachmentPool, Pool: poolOf("flag{a}", "flag{b}")}
	static := models.AttachmentConfig{AttachName: "x", AttachType: models.AttachmentTypeStaticFile}

	cases := []struct {
		name      string
		challenge *models.Challenge
		want      error
	}{
		{"valid pool", poolChallenge(pool), nil},
		{"static challenge without pool", &models.Challenge{FlagType: models.FlagTypeStatic, Attachments: models.AttachmentConfigs{static}}, nil},
		{"pool flag without pool attachment", poolChallenge(static), ErrPoolFlagTypeMismatch},
		{"pool attachment with dynamic flag", &models.Challenge{FlagType: models.FlagTypeDynamic, Attachments: models.AttachmentConfigs{pool}}, ErrPoolFlagTypeMismatch},
		{"pool plus another attachment", poolChallenge(pool, static), ErrPoolMustBeOnly},
		{"two pools", poolChallenge(pool, pool), ErrPoolMustBeOnly},
		{"empty pool", poolChallenge(models.AttachmentConfig{AttachType: models.AttachmentTypeAttachmentPool}), ErrPoolEmpty},
		{"entry without flag", poolChallenge(models.AttachmentConfig{AttachType: models.AttachmentTypeAttachmentPool, Pool: poolOf(" ")}), ErrPoolInvalidEntry},
	}
	for _, tc := range cases {
		if got := Validate(tc.challenge); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRemovedFlags(t *testing.T) {
	removed := RemovedFlags(poolOf("a", "b", "c"), poolOf("a", "c2", "d"))
	if len(removed) != 2 || removed[0] != "b" || removed[1] != "c" {
		t.Fatalf("unexpected removed flags: %v", removed)
	}
	if got := RemovedFlags(poolOf("a"), poolOf("a", "b")); len(got) != 0 {
		t.Fatalf("adding entries should remove nothing, got %v", got)
	}
}

func TestEntryForFlag(t *testing.T) {
	pool := poolOf("a", "b")
	if e := EntryForFlag(pool, "b"); e == nil || e.FileID != "file-1" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if e := EntryForFlag(pool, "missing"); e != nil {
		t.Fatalf("expected nil for removed entry, got %+v", e)
	}
	if i := EntryIndexForFlag(pool, "missing"); i != -1 {
		t.Fatalf("expected -1, got %d", i)
	}
}
