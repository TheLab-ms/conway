package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"golang.org/x/oauth2"
)

type discordAPIClient struct {
	botToken, guildID string
	client            *http.Client
	authConf          *oauth2.Config
}

func newDiscordAPIClient(botToken, guildID string, client *http.Client, authConf *oauth2.Config) *discordAPIClient {
	return &discordAPIClient{
		botToken: botToken,
		guildID:  guildID,
		client:   client,
		authConf: authConf,
	}
}

func (c *discordAPIClient) EnsureRole(ctx context.Context, userID string, roleID string, inRole bool) (changed bool, info GuildMemberInfo, err error) {
	info, err = c.GetGuildMember(ctx, userID, roleID)
	if err != nil {
		return false, GuildMemberInfo{}, fmt.Errorf("checking current role status: %w", err)
	}
	if info.HasRole == inRole {
		return false, info, nil
	}

	if inRole {
		err = c.AddRole(ctx, userID, roleID)
	} else {
		err = c.RemoveRole(ctx, userID, roleID)
	}
	return true, info, err
}

func (c *discordAPIClient) makeDiscordAPIRequest(ctx context.Context, method, endpoint string) (*http.Response, error) {
	url := fmt.Sprintf("https://discord.com/api/v10%s", endpoint)
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}

	return resp, nil
}

func (c *discordAPIClient) HasRole(ctx context.Context, userID string, roleID string) (bool, error) {
	info, err := c.GetGuildMember(ctx, userID, roleID)
	if err != nil {
		return false, err
	}
	return info.HasRole, nil
}

type GuildMemberInfo struct {
	HasRole     bool
	DisplayName string // nick > global_name > username
	Avatar      []byte
}

func (c *discordAPIClient) fetchAvatar(ctx context.Context, avatarURL string) ([]byte, error) {
	if avatarURL == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", avatarURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating avatar request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching avatar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("avatar fetch error: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *discordAPIClient) GetGuildMember(ctx context.Context, userID string, roleID string) (GuildMemberInfo, error) {
	endpoint := fmt.Sprintf("/guilds/%s/members/%s", c.guildID, userID)
	resp, err := c.makeDiscordAPIRequest(ctx, "GET", endpoint)
	if err != nil {
		return GuildMemberInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return GuildMemberInfo{}, nil
	}
	if resp.StatusCode != 200 {
		return GuildMemberInfo{}, fmt.Errorf("discord API error: %d", resp.StatusCode)
	}

	var member struct {
		Nick   string   `json:"nick"`
		Avatar string   `json:"avatar"`
		Roles  []string `json:"roles"`
		User   struct {
			ID         string `json:"id"`
			Username   string `json:"username"`
			GlobalName string `json:"global_name"`
			Avatar     string `json:"avatar"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&member); err != nil {
		return GuildMemberInfo{}, fmt.Errorf("decoding response: %w", err)
	}

	// Priority: nick > global_name > username
	displayName := member.Nick
	if displayName == "" {
		displayName = member.User.GlobalName
	}
	if displayName == "" {
		displayName = member.User.Username
	}

	// Priority: guild-specific avatar > user avatar
	var avatarURL string
	if member.Avatar != "" {
		avatarURL = fmt.Sprintf("https://cdn.discordapp.com/guilds/%s/users/%s/avatars/%s.png", c.guildID, member.User.ID, member.Avatar)
	} else if member.User.Avatar != "" {
		avatarURL = fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png", member.User.ID, member.User.Avatar)
	}

	avatar, err := c.fetchAvatar(ctx, avatarURL)
	if err != nil {
		return GuildMemberInfo{}, fmt.Errorf("fetching avatar: %w", err)
	}

	return GuildMemberInfo{
		HasRole:     slices.Contains(member.Roles, roleID),
		DisplayName: displayName,
		Avatar:      avatar,
	}, nil
}

func (c *discordAPIClient) AddRole(ctx context.Context, userID string, roleID string) error {
	endpoint := fmt.Sprintf("/guilds/%s/members/%s/roles/%s", c.guildID, userID, roleID)
	resp, err := c.makeDiscordAPIRequest(ctx, "PUT", endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 || resp.StatusCode < 200 {
		return fmt.Errorf("discord API error: %d", resp.StatusCode)
	}
	return nil
}

func (c *discordAPIClient) RemoveRole(ctx context.Context, userID string, roleID string) error {
	endpoint := fmt.Sprintf("/guilds/%s/members/%s/roles/%s", c.guildID, userID, roleID)
	resp, err := c.makeDiscordAPIRequest(ctx, "DELETE", endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 || resp.StatusCode < 200 {
		return fmt.Errorf("discord API error: %d", resp.StatusCode)
	}
	return nil
}

type DiscordUserInfo struct {
	ID     string
	Email  string
	Avatar []byte
}

func (c *discordAPIClient) GetUserInfo(ctx context.Context, token *oauth2.Token) (DiscordUserInfo, error) {
	client := c.authConf.Client(ctx, token)
	resp, err := client.Get("https://discord.com/api/users/@me")
	if err != nil {
		return DiscordUserInfo{}, fmt.Errorf("fetching Discord user info: %w", err)
	}
	defer resp.Body.Close()

	var user struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		Avatar string `json:"avatar"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return DiscordUserInfo{}, fmt.Errorf("decoding Discord user response: %w", err)
	}

	var avatarURL string
	if user.Avatar != "" {
		avatarURL = fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png", user.ID, user.Avatar)
	}

	avatar, err := c.fetchAvatar(ctx, avatarURL)
	if err != nil {
		return DiscordUserInfo{}, fmt.Errorf("fetching avatar: %w", err)
	}

	return DiscordUserInfo{
		ID:     user.ID,
		Email:  user.Email,
		Avatar: avatar,
	}, nil
}
