package models

import "time"

// Settings represents application settings.
type Settings struct {
	General       GeneralSettings      `json:"general"`
	Notifications NotificationSettings `json:"notifications"`
	Security      SecuritySettings     `json:"security"`
}

// GeneralSettings represents general application settings.
type GeneralSettings struct {
	Timezone string `json:"timezone"`
	Language string `json:"language"`
	Theme    string `json:"theme"`
}

// NotificationSettings represents notification settings.
type NotificationSettings struct {
	Email      bool   `json:"email"`
	Webhook    bool   `json:"webhook"`
	WebhookURL string `json:"webhookUrl"`
}

// SecuritySettings represents security settings.
type SecuritySettings struct {
	SessionTimeout   int  `json:"sessionTimeout"`
	TwoFactorEnabled bool `json:"twoFactorEnabled"`
}

// SettingEntry represents a single setting in the database.
type SettingEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}
