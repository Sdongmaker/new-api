package model

const (
	BootstrapDeviceStatusActive  = "active"
	BootstrapDeviceStatusBlocked = "blocked"
)

type BootstrapDevice struct {
	ID                    int    `json:"id" gorm:"primaryKey"`
	InstallIDHash         string `json:"install_id_hash" gorm:"type:varchar(64);uniqueIndex;not null"`
	DeviceFingerprintHash string `json:"device_fingerprint_hash" gorm:"type:varchar(64);uniqueIndex;not null"`
	UserID                int    `json:"user_id" gorm:"column:user_id;index"`
	TokenID               int    `json:"token_id" gorm:"column:token_id;index"`
	Status                string `json:"status" gorm:"type:varchar(32);index;default:'active'"`
	RiskFlags             string `json:"risk_flags" gorm:"type:text"`
	FirstIP               string `json:"first_ip" gorm:"type:varchar(64)"`
	LastIP                string `json:"last_ip" gorm:"type:varchar(64)"`
	UserAgent             string `json:"user_agent" gorm:"type:varchar(255)"`
	ClientVersion         string `json:"client_version" gorm:"type:varchar(64)"`
	Platform              string `json:"platform" gorm:"type:varchar(32)"`
	Arch                  string `json:"arch" gorm:"type:varchar(32)"`
	CreatedAt             int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt             int64  `json:"updated_at" gorm:"autoUpdateTime"`
	LastSeenAt            int64  `json:"last_seen_at" gorm:"index"`
}

func (BootstrapDevice) TableName() string {
	return "bootstrap_devices"
}
