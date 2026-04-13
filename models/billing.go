package models

import "time"

type Payment struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	OrderCode   int64     `json:"order_code"`
	Amount      int       `json:"amount"`
	PackageName string    `json:"package_name"`
	Status      string    `json:"status"` // PENDING, PAID, CANCELLED
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SystemSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
