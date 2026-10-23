package models

type Category struct {
	// Standard ID field
	ID           uint   `gorm:"primaryKey;column:category_id" json:"category_id"`
	CategoryName string `gorm:"column:category_name;not null" json:"category_name"`
	Description  string `gorm:"column:description" json:"description"`
}
