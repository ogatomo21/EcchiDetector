package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	vercelProvider          = "vercel"
	typesafeProvider        = "typesafe"
	netlifyProvider         = "netlify"
	typesafeModel           = "jev-latest"
	vercelModel             = "typesafe-ai/jev"
	threshold               = 0.20
	presenceText            = "えっちなことはやめてください！！！"
	vercelEndpoint          = "https://ai-gateway.vercel.sh/v1/evaluate"
	typesafeEndpoint        = "https://api.typesafe.ai/v1/systemone"
	userMessageLimit        = 20
	userMessageWindow       = time.Minute
	guildMessageLimit       = 60
	userReplyCooldown       = 30 * time.Second
	messageScoreCommandName = "えっち度を見る"
	welcomeText             = "えっち感知器です！\nサーバーのメッセージ本文を設定されたJev提供経路に送り、性的なニュアンスを判定します。DMとBotのメッセージは判定しません。設定しきい値以上のメッセージにはスコアに応じた確率で返信し、文末にえっち度を表示します。返信元には👀を付けます。同じユーザーへの返信は30秒に1回までです。初期しきい値は0.20です。\n返信がなかったメッセージも、右クリック（スマホでは長押し）の「アプリ」→「えっち度を見る」から、自分だけに見える判定結果を確認できます。\nユーザーごとに1分20件、サーバーごとに1分60件まで判定します。管理者は `/ecchi` の `threshold` でしきい値変更、`ignore` / `allow` でチャンネル除外を設定できます。"
)

type replyVariant struct {
	text      string
	emojiOnly bool
}

var replyVariants = [...]replyVariant{
	{text: "えっちですよ！！！"},
	{text: "そういうのよくないとおもいます！！！"},
	{text: "汚い話はやめてください！！！"},
	{text: "😏", emojiOnly: true},
	{text: "🙄", emojiOnly: true},
	{text: "🤓", emojiOnly: true},
}

type guildConfig struct {
	Threshold       *float64 `json:"threshold,omitempty"`
	IgnoredChannels []string `json:"ignored_channels,omitempty"`
	WelcomeSent     bool     `json:"welcome_sent,omitempty"`
}

type configStore struct {
	mu     sync.RWMutex
	path   string
	guilds map[string]guildConfig
}

type messageRateLimiter struct {
	mu            sync.Mutex
	messages      map[string][]time.Time
	guildMessages map[string][]time.Time
	replies       map[string]time.Time
	lastCleanup   time.Time
}

func newMessageRateLimiter() *messageRateLimiter {
	return &messageRateLimiter{
		messages:      make(map[string][]time.Time),
		guildMessages: make(map[string][]time.Time),
		replies:       make(map[string]time.Time),
	}
}

func (l *messageRateLimiter) allow(userID, guildID string) bool {
	if userID == "" || guildID == "" {
		return false
	}
	now := time.Now()
	cutoff := now.Add(-userMessageWindow)
	l.mu.Lock()
	defer l.mu.Unlock()
	// ponytail: in-memory limits reset on restart; use a shared limiter if running multiple instances.
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= userMessageWindow {
		for id, timestamps := range l.messages {
			if len(timestamps) == 0 || !timestamps[len(timestamps)-1].After(cutoff) {
				delete(l.messages, id)
			}
		}
		for id, timestamps := range l.guildMessages {
			if len(timestamps) == 0 || !timestamps[len(timestamps)-1].After(cutoff) {
				delete(l.guildMessages, id)
			}
		}
		replyCutoff := now.Add(-userReplyCooldown)
		for id, lastReply := range l.replies {
			if !lastReply.After(replyCutoff) {
				delete(l.replies, id)
			}
		}
		l.lastCleanup = now
	}
	timestamps := l.messages[userID]
	first := 0
	for first < len(timestamps) && !timestamps[first].After(cutoff) {
		first++
	}
	timestamps = timestamps[first:]
	if len(timestamps) >= userMessageLimit {
		l.messages[userID] = timestamps
		return false
	}
	guildTimestamps := l.guildMessages[guildID]
	guildFirst := 0
	for guildFirst < len(guildTimestamps) && !guildTimestamps[guildFirst].After(cutoff) {
		guildFirst++
	}
	guildTimestamps = guildTimestamps[guildFirst:]
	if len(guildTimestamps) >= guildMessageLimit {
		if len(timestamps) == 0 {
			delete(l.messages, userID)
		} else {
			l.messages[userID] = timestamps
		}
		l.guildMessages[guildID] = guildTimestamps
		return false
	}
	l.messages[userID] = append(timestamps, now)
	l.guildMessages[guildID] = append(guildTimestamps, now)
	return true
}

func (l *messageRateLimiter) allowReply(userID string) bool {
	if userID == "" {
		return false
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if lastReply, ok := l.replies[userID]; ok && now.Sub(lastReply) < userReplyCooldown {
		return false
	}
	l.replies[userID] = now
	return true
}

func replyProbability(score float64) float64 {
	if score < 0.10 {
		return 0
	}
	tier := int(score * 10)
	if tier > 10 {
		tier = 10
	}
	return float64(tier) / 10
}

func loadConfig(path string) (*configStore, error) {
	config := &configStore{path: path, guilds: make(map[string]guildConfig)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &config.guilds); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	if config.guilds == nil {
		config.guilds = make(map[string]guildConfig)
	}
	return config, nil
}

func configPath() string {
	if path := strings.TrimSpace(os.Getenv("CONFIG_PATH")); path != "" {
		return path
	}
	if dir := os.Getenv("STATE_DIRECTORY"); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	return filepath.Join("data", "config.json")
}

func cachePath() string {
	if path := strings.TrimSpace(os.Getenv("CACHE_PATH")); path != "" {
		return path
	}
	if dir := os.Getenv("STATE_DIRECTORY"); dir != "" {
		return filepath.Join(dir, "classification-cache.db")
	}
	return filepath.Join("data", "classification-cache.db")
}

func (c *configStore) saveLocked() error {
	data, err := json.MarshalIndent(c.guilds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0600)
}

func (c *configStore) update(guildID string, change func(*guildConfig) bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous, existed := c.guilds[guildID]
	updated := previous
	updated.IgnoredChannels = append([]string(nil), previous.IgnoredChannels...)
	if !change(&updated) {
		return nil
	}
	c.guilds[guildID] = updated
	if err := c.saveLocked(); err != nil {
		if existed {
			c.guilds[guildID] = previous
		} else {
			delete(c.guilds, guildID)
		}
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (c *configStore) settings(guildID string) guildConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	settings := c.guilds[guildID]
	settings.IgnoredChannels = append([]string(nil), settings.IgnoredChannels...)
	if settings.Threshold != nil {
		value := *settings.Threshold
		settings.Threshold = &value
	}
	return settings
}

func (c *configStore) thresholdFor(guildID string) float64 {
	value := c.settings(guildID).Threshold
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1 {
		return threshold
	}
	return *value
}

func (c *configStore) channelIgnored(guildID, channelID string) bool {
	for _, id := range c.settings(guildID).IgnoredChannels {
		if id == channelID {
			return true
		}
	}
	return false
}

func ecchiCommand() *discordgo.ApplicationCommand {
	manageGuild := int64(discordgo.PermissionManageGuild)
	minimum := 0.0
	return &discordgo.ApplicationCommand{
		Name:                     "ecchi",
		Description:              "えっち感知器のサーバー設定",
		DefaultMemberPermissions: &manageGuild,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "threshold", Description: "検知しきい値を0から1で設定",
				Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionNumber, Name: "value", Description: "確率しきい値 (0.0-1.0)", Required: true, MinValue: &minimum, MaxValue: 1}},
			},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "ignore", Description: "チャンネルを検知対象から除外",
				Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "除外するチャンネル", Required: true, ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews, discordgo.ChannelTypeGuildForum}}},
			},
			{
				Type: discordgo.ApplicationCommandOptionSubCommand, Name: "allow", Description: "除外したチャンネルで再び検知",
				Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "対象に戻すチャンネル", Required: true, ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews, discordgo.ChannelTypeGuildForum}}},
			},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "settings", Description: "現在のサーバー設定を表示"},
		},
	}
}

func registerCommands(s *discordgo.Session) error {
	if s.State == nil || s.State.User == nil {
		return fmt.Errorf("Discord application ID is unavailable")
	}
	_, err := s.ApplicationCommandCreate(s.State.User.ID, "", ecchiCommand())
	if err != nil {
		return err
	}
	_, err = s.ApplicationCommandCreate(s.State.User.ID, "", &discordgo.ApplicationCommand{
		Type: discordgo.MessageApplicationCommand,
		Name: messageScoreCommandName,
	})
	return err
}

func commandOption(options []*discordgo.ApplicationCommandInteractionDataOption, name string) *discordgo.ApplicationCommandInteractionDataOption {
	for _, option := range options {
		if option.Name == name {
			return option
		}
	}
	return nil
}

func respondCommand(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	})
	if err != nil {
		log.Printf("respond to interaction: %v", err)
	}
}

func messageScoreCommand(s *discordgo.Session, i *discordgo.InteractionCreate, data discordgo.ApplicationCommandInteractionData, jev *jevClient, limiter *messageRateLimiter) {
	if i.GuildID == "" || data.Resolved == nil || data.Resolved.Messages[data.TargetID] == nil {
		respondCommand(s, i, "対象のメッセージを読み取れませんでした。サーバー内のメッセージを選択してください。")
		return
	}
	content := strings.TrimSpace(data.Resolved.Messages[data.TargetID].Content)
	if content == "" {
		respondCommand(s, i, "対象のメッセージに判定できる本文がありません。")
		return
	}
	var userID string
	if i.Member != nil && i.Member.User != nil {
		userID = i.Member.User.ID
	} else if i.User != nil {
		userID = i.User.ID
	}
	if !limiter.allow(userID, i.GuildID) {
		respondCommand(s, i, "判定回数の上限です。1分ほど待ってからもう一度お試しください。")
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("defer message score interaction: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	score, cached, err := jev.score(ctx, content)
	response := fmt.Sprintf("えっち度: %d%%", int(math.Round(score*100)))
	if err != nil {
		log.Printf("classify selected message %s: %v", data.TargetID, err)
		response = "判定に失敗しました。しばらくしてからもう一度お試しください。"
	} else if jev.debug {
		source := "api"
		if cached {
			source = "cache"
		}
		log.Printf("debug selected message %s: sexual_suggestiveness=%.4f source=%s", data.TargetID, score, source)
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &response}); err != nil {
		log.Printf("send selected message score: %v", err)
	}
}

func interactionCreate(s *discordgo.Session, i *discordgo.InteractionCreate, config *configStore, jev *jevClient, limiter *messageRateLimiter) {
	data, ok := i.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok {
		respondCommand(s, i, "コマンドを読み取れませんでした。")
		return
	}
	if data.CommandType == discordgo.MessageApplicationCommand && data.Name == messageScoreCommandName {
		messageScoreCommand(s, i, data, jev, limiter)
		return
	}
	if i.GuildID == "" || i.Member == nil || i.Member.Permissions&(discordgo.PermissionManageGuild|discordgo.PermissionAdministrator) == 0 {
		respondCommand(s, i, "このコマンドはサーバー管理権限が必要です。")
		return
	}
	if len(data.Options) == 0 {
		respondCommand(s, i, "コマンドを読み取れませんでした。")
		return
	}
	subcommand := data.Options[0]
	var message string
	switch subcommand.Name {
	case "threshold":
		option := commandOption(subcommand.Options, "value")
		if option == nil {
			message = "しきい値が指定されていません。"
			break
		}
		value := option.FloatValue()
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			message = "しきい値は0.0から1.0で指定してください。"
			break
		}
		if err := config.update(i.GuildID, func(settings *guildConfig) bool {
			settings.Threshold = &value
			return true
		}); err != nil {
			message = "設定を保存できませんでした。管理者にログを確認してもらってください。"
		} else {
			message = fmt.Sprintf("このサーバーの検知しきい値を %.2f にしました。", value)
		}
	case "ignore", "allow":
		option := commandOption(subcommand.Options, "channel")
		if option == nil {
			message = "チャンネルが指定されていません。"
			break
		}
		channelID, ok := option.Value.(string)
		if !ok || channelID == "" {
			message = "チャンネルを読み取れませんでした。"
			break
		}
		ignore := subcommand.Name == "ignore"
		if err := config.update(i.GuildID, func(settings *guildConfig) bool {
			found := false
			for _, id := range settings.IgnoredChannels {
				if id == channelID {
					found = true
					break
				}
			}
			if ignore && !found {
				settings.IgnoredChannels = append(settings.IgnoredChannels, channelID)
				return true
			}
			if !ignore && found {
				filtered := settings.IgnoredChannels[:0]
				for _, id := range settings.IgnoredChannels {
					if id != channelID {
						filtered = append(filtered, id)
					}
				}
				settings.IgnoredChannels = filtered
				return true
			}
			return false
		}); err != nil {
			message = "設定を保存できませんでした。管理者にログを確認してもらってください。"
		} else if ignore {
			message = fmt.Sprintf("<#%s> を検知対象から除外しました。", channelID)
		} else {
			message = fmt.Sprintf("<#%s> を検知対象に戻しました。", channelID)
		}
	case "settings":
		settings := config.settings(i.GuildID)
		channelIDs := append([]string(nil), settings.IgnoredChannels...)
		sort.Strings(channelIDs)
		ignored := "なし"
		if len(channelIDs) > 0 {
			mentions := make([]string, len(channelIDs))
			for index, id := range channelIDs {
				mentions[index] = "<#" + id + ">"
			}
			ignored = strings.Join(mentions, ", ")
		}
		message = fmt.Sprintf("検知しきい値: %.2f\n除外チャンネル: %s", config.thresholdFor(i.GuildID), ignored)
	default:
		message = "未対応の設定です。"
	}
	respondCommand(s, i, message)
}

type jevRequest struct {
	Model           string         `json:"model"`
	State           string         `json:"state"`
	Questions       map[string]any `json:"questions"`
	ProviderOptions map[string]any `json:"providerOptions,omitempty"`
}

type jevResponse struct {
	Answers map[string]struct {
		Noul        *float64 `json:"noul"`
		Probability *float64 `json:"probability"`
	} `json:"answers"`
}

func newJevRequest(content, model string, gateway bool) jevRequest {
	request := jevRequest{Model: model, State: content, Questions: map[string]any{
		"sexual_suggestiveness": map[string]any{
			"type":         "noul",
			"instructions": "このDiscordメッセージに、性的な話題、下ネタ、性的な冗談、軽い性的ニュアンス、思春期の人が「えっち」と感じる言い回しが含まれているか判定してください。直接的な表現に限らず、些細な匂わせや文脈上の性的な含みもtrueにしてください。単なる日常会話や性的な意味のない表現はfalseです。",
			"criteria": map[string]string{
				"true":  "些細なものを含め、性的な話題、下ネタ、性的な冗談、匂わせ、または性的な含みがある",
				"false": "性的な意味やニュアンスがない普通の内容",
			},
		},
	}}
	if gateway {
		question := request.Questions["sexual_suggestiveness"].(map[string]any)
		request.Model = vercelModel
		request.Questions["sexual_suggestiveness"] = map[string]any{"type": "boolean", "instructions": question["instructions"]}
		request.ProviderOptions = map[string]any{"gateway": map[string]any{"only": []string{"typesafe-ai"}}}
	}
	return request
}

func (c *jevClient) score(ctx context.Context, content string) (float64, bool, error) {
	normalized := normalizeForCache(content)
	key := cacheKey(c.provider, c.model, normalized)
	// ponytail: simultaneous misses for the same key can duplicate Jev calls; add singleflight if bursts make it material.
	if c.cache != nil && normalized != "" {
		if score, ok, err := c.cache.get(ctx, key); err != nil {
			log.Printf("read classification cache: %v", err)
		} else if ok {
			return score, true, nil
		}
	}
	body, err := json.Marshal(newJevRequest(content, c.model, c.provider == vercelProvider))
	if err != nil {
		return 0, false, fmt.Errorf("encode Jev request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, false, fmt.Errorf("create Jev request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("call %s: %w", c.providerName, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var gatewayError struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&gatewayError)
		if gatewayError.Error.Message != "" {
			return 0, false, fmt.Errorf("%s returned HTTP %d: %s", c.providerName, res.StatusCode, gatewayError.Error.Message)
		}
		if gatewayError.Message != "" {
			return 0, false, fmt.Errorf("%s returned HTTP %d: %s", c.providerName, res.StatusCode, gatewayError.Message)
		}
		return 0, false, fmt.Errorf("%s returned HTTP %d", c.providerName, res.StatusCode)
	}
	var result jevResponse
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return 0, false, fmt.Errorf("decode Jev response: %w", err)
	}
	answer, ok := result.Answers["sexual_suggestiveness"]
	if !ok {
		return 0, false, fmt.Errorf("Jev response has no valid sexual_suggestiveness answer")
	}
	score := answer.Noul
	if c.provider == vercelProvider {
		score = answer.Probability
	}
	if score == nil || *score < 0 || *score > 1 {
		return 0, false, fmt.Errorf("Jev response has no valid sexual_suggestiveness answer")
	}
	if c.cache != nil && normalized != "" {
		if err := c.cache.put(ctx, key, *score); err != nil {
			log.Printf("write classification cache: %v", err)
		}
	}
	return *score, false, nil
}

type jevClient struct {
	provider     string
	providerName string
	model        string
	endpoint     string
	apiKey       string
	httpClient   *http.Client
	cache        *scoreCache
	debug        bool
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate, jev *jevClient, config *configStore, limiter *messageRateLimiter) {
	if m.GuildID == "" || m.Author == nil || m.Author.Bot || strings.TrimSpace(m.Content) == "" {
		return
	}
	if config.channelIgnored(m.GuildID, m.ChannelID) {
		return
	}
	if channel, err := s.State.Channel(m.ChannelID); err == nil && channel.ParentID != "" && config.channelIgnored(m.GuildID, channel.ParentID) {
		return
	}
	if !limiter.allow(m.Author.ID, m.GuildID) {
		if jev.debug {
			log.Printf("debug message %s: skipped=rate-limit", m.ID)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	content := strings.TrimSpace(m.Content)
	score, cached, err := jev.score(ctx, content)
	if err != nil {
		log.Printf("classify message %s: %v", m.ID, err)
		return
	}
	if jev.debug {
		source := "api"
		if cached {
			source = "cache"
		}
		log.Printf("debug message %s: sexual_suggestiveness=%.4f reply_probability=%.0f%% source=%s", m.ID, score, replyProbability(score)*100, source)
	}
	if score < config.thresholdFor(m.GuildID) {
		return
	}
	probability := replyProbability(score)
	if probability == 0 || rand.Float64() >= probability {
		return
	}
	if !limiter.allowReply(m.Author.ID) {
		if jev.debug {
			log.Printf("debug message %s: skipped=reply-cooldown", m.ID)
		}
		return
	}
	variant := replyVariants[rand.Intn(len(replyVariants))]
	scoreText := fmt.Sprintf("(えっち度: %d%%)", int(math.Round(score*100)))
	response := variant.text
	if !variant.emojiOnly {
		response += "\n" + scoreText
	}
	_, err = s.ChannelMessageSendReply(m.ChannelID, response, &discordgo.MessageReference{
		MessageID: m.ID,
		ChannelID: m.ChannelID,
		GuildID:   m.GuildID,
	})
	if err != nil {
		log.Printf("reply to message %s: %v", m.ID, err)
	} else if variant.emojiOnly {
		if _, err := s.ChannelMessageSend(m.ChannelID, scoreText); err != nil {
			log.Printf("send score for message %s: %v", m.ID, err)
		}
	}
	if err := s.MessageReactionAdd(m.ChannelID, m.ID, "👀"); err != nil {
		log.Printf("react to message %s: %v", m.ID, err)
	}
}

func guildCreate(s *discordgo.Session, event *discordgo.GuildCreate, config *configStore) {
	if event.Guild == nil || event.ID == "" || config.settings(event.ID).WelcomeSent {
		return
	}
	channelIDs := []string{event.SystemChannelID}
	for _, channel := range event.Channels {
		if channel != nil && (channel.Type == discordgo.ChannelTypeGuildText || channel.Type == discordgo.ChannelTypeGuildNews) {
			channelIDs = append(channelIDs, channel.ID)
		}
	}
	seen := make(map[string]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID == "" {
			continue
		}
		if _, exists := seen[channelID]; exists {
			continue
		}
		seen[channelID] = struct{}{}
		if _, err := s.ChannelMessageSend(channelID, welcomeText); err != nil {
			continue
		}
		if err := config.update(event.ID, func(settings *guildConfig) bool {
			settings.WelcomeSent = true
			return true
		}); err != nil {
			log.Printf("save welcome status for guild %s: %v", event.ID, err)
		}
		return
	}
	log.Printf("could not send welcome message to guild %s", event.ID)
}

func requiredEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		log.Fatalf("%s is not set", name)
	}
	return value
}

func netlifyEndpoint(functionURL string) (string, error) {
	endpoint, err := url.Parse(strings.TrimSpace(functionURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return "", fmt.Errorf("NETLIFY_FUNCTION_URL must be an HTTPS URL without credentials, query, or fragment")
	}
	return endpoint.String(), nil
}

func main() {
	log.SetPrefix("EcchiDetector: ")
	debug := flag.Bool("debug", false, "ログにメッセージごとの判定スコアを出力する")
	flag.Parse()

	provider := strings.ToLower(strings.TrimSpace(os.Getenv("AI_PROVIDER")))
	if provider == "" {
		provider = vercelProvider
	}
	jev := &jevClient{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		debug:      *debug,
	}
	switch provider {
	case vercelProvider:
		jev.provider, jev.providerName, jev.model, jev.endpoint = vercelProvider, "Vercel AI Gateway (TypeSafe only)", vercelModel, vercelEndpoint
	case typesafeProvider:
		jev.provider, jev.providerName, jev.model, jev.endpoint = typesafeProvider, "TypeSafe API", typesafeModel, typesafeEndpoint
	case netlifyProvider:
		jev.provider, jev.providerName, jev.model = netlifyProvider, "Netlify Function / AI Gateway (TypeSafe)", typesafeModel
		var err error
		jev.endpoint, err = netlifyEndpoint(requiredEnv("NETLIFY_FUNCTION_URL"))
		if err != nil {
			log.Fatal(err)
		}
		jev.httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	default:
		log.Fatalf("AI_PROVIDER must be %q, %q, or %q", vercelProvider, typesafeProvider, netlifyProvider)
	}
	jev.apiKey = requiredEnv("AI_PROVIDER_API_KEY")
	cache, err := openScoreCache(cachePath())
	if err != nil {
		log.Printf("SQLite classification cache unavailable; continuing without cache: %v", err)
	} else {
		jev.cache = cache
		defer func() {
			if err := cache.close(); err != nil {
				log.Printf("close SQLite classification cache: %v", err)
			}
		}()
	}
	config, err := loadConfig(configPath())
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	session, err := discordgo.New("Bot " + requiredEnv("DISCORD_TOKEN"))
	if err != nil {
		log.Fatalf("create Discord session: %v", err)
	}
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent
	limiter := newMessageRateLimiter()
	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) { messageCreate(s, m, jev, config, limiter) })
	session.AddHandler(func(s *discordgo.Session, event *discordgo.GuildCreate) { guildCreate(s, event, config) })
	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		interactionCreate(s, i, config, jev, limiter)
	})
	session.AddHandlerOnce(func(s *discordgo.Session, _ *discordgo.Ready) {
		if err := registerCommands(s); err != nil {
			log.Printf("register /ecchi command: %v", err)
		}
	})
	if err := session.Open(); err != nil {
		log.Fatalf("open Discord session: %v", err)
	}
	if err := session.UpdateGameStatus(0, presenceText); err != nil {
		log.Printf("failed to set presence: %v", err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			log.Printf("close Discord session: %v", err)
		}
	}()

	log.Print("EcchiDetector is running")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	log.Print("shutting down")
}
