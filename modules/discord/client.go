package discord

import (
	"context"
	"encoding/json"
	"fmt"
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

func (c *discordAPIClient) EnsureRole(ctx context.Context, userID string, roleID string, inRole bool) (bool, error) {
	hasRole, err := c.HasRole(ctx, userID, roleID)
	if err != nil {
		return false, fmt.Errorf("checking current role status: %w", err)
	}
	if hasRole == inRole {
		return false, nil
	}

	if inRole {
		err = c.AddRole(ctx, userID, roleID)
	} else {
		err = c.RemoveRole(ctx, userID, roleID)
	}
	return true, err
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
	endpoint := fmt.Sprintf("/guilds/%s/members/%s", c.guildID, userID)
	resp, err := c.makeDiscordAPIRequest(ctx, "GET", endpoint)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return false, nil
	}
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("discord API error: %d", resp.StatusCode)
	}

	var member struct {
		Roles []string `json:"roles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&member); err != nil {
		return false, fmt.Errorf("decoding response: %w", err)
	}

	return slices.Contains(member.Roles, roleID), nil
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

func (c *discordAPIClient) GetUserInfo(ctx context.Context, token *oauth2.Token) (string, error) {
	client := c.authConf.Client(ctx, token)
	resp, err := client.Get("https://discord.com/api/users/@me")
	if err != nil {
		return "", fmt.Errorf("fetching Discord user info: %w", err)
	}
	defer resp.Body.Close()

	var user struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decoding Discord user response: %w", err)
	}

	return user.ID, nil
}
