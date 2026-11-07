package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const jevPromptVersion = "sexual-suggestiveness-v1"

var (
	discordMentionPattern = regexp.MustCompile(`<[@#][!&]?[0-9]+>`)
	plainMentionPattern   = regexp.MustCompile(`(^|[\s[:punct:]])@[\p{L}\p{N}_.-]+`)
)

type scoreCache struct {
	db *sql.DB
}

func normalizeForCache(content string) string {
	content = discordMentionPattern.ReplaceAllString(content, " ")
	content = plainMentionPattern.ReplaceAllString(content, "$1")
	var normalized strings.Builder
	var previousPunctuation rune
	for _, r := range content {
		r = unicode.ToLower(r)
		if unicode.IsPunct(r) {
			if r == previousPunctuation {
				continue
			}
			previousPunctuation = r
		} else {
			previousPunctuation = 0
		}
		normalized.WriteRune(r)
	}
	return strings.Join(strings.Fields(normalized.String()), " ")
}

func cacheKey(provider, model, content string) string {
	input := provider + "\x00" + model + "\x00" + jevPromptVersion + "\x00" + content
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func openScoreCache(path string) (*scoreCache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// ponytail: entries do not expire; add age-based cleanup if the cache grows materially.
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS classification_cache (
		cache_key TEXT PRIMARY KEY,
		score REAL NOT NULL CHECK (score >= 0 AND score <= 1)
	) WITHOUT ROWID`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &scoreCache{db: db}, nil
}

func (c *scoreCache) get(ctx context.Context, key string) (float64, bool, error) {
	var score float64
	err := c.db.QueryRowContext(ctx, "SELECT score FROM classification_cache WHERE cache_key = ?", key).Scan(&score)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return score, err == nil, err
}

func (c *scoreCache) put(ctx context.Context, key string, score float64) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO classification_cache (cache_key, score) VALUES (?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET score = excluded.score`, key, score)
	return err
}

func (c *scoreCache) close() error {
	return c.db.Close()
}
