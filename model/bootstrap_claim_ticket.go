package model

type BootstrapClaimTicket struct {
	ID           int    `json:"id" gorm:"primaryKey"`
	TicketHash   string `json:"ticket_hash" gorm:"type:varchar(64);uniqueIndex;not null"`
	UserID       int    `json:"user_id" gorm:"column:user_id;index;not null"`
	DeviceID     int    `json:"device_id" gorm:"column:device_id;index;not null"`
	RedirectPath string `json:"redirect_path" gorm:"type:varchar(512)"`
	ExpiresAt    int64  `json:"expires_at" gorm:"index;not null"`
	ConsumedAt   int64  `json:"consumed_at" gorm:"index;default:0"`
	CreatedAt    int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt    int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (BootstrapClaimTicket) TableName() string {
	return "bootstrap_claim_tickets"
}
