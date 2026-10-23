package models

import (
	"fmt"

	"gorm.io/gorm"
)

func CategorySeeder(db *gorm.DB) {
	// 1. Check if categories already exist to avoid duplicates
	var count int64
	db.Model(&Category{}).Count(&count)

	if count == 0 {
		// 2. Define your default data
		categories := []Category{
			{CategoryName: "Programming & Development", Description: "Web, Mobile, and Software development"},
			{CategoryName: "Design & Creative", Description: "UI/UX, Graphic Design, and creative arts"},
			{CategoryName: "Business & Marketing", Description: "Marketing, Finance, and Sales strategies"},
			{CategoryName: "Data Science & Analytics", Description: "Data analysis, Machine Learning, and Statistics"},
			{CategoryName: "Personal Development", Description: "Soft skills, productivity, and leadership"},
			{CategoryName: "Health & Fitness", Description: "Physical health, nutrition, and mental well-being"},
			{CategoryName: "Language Learning", Description: "Grammar, vocabulary, and linguistics"},
			{CategoryName: "Other", Description: "Miscellaneous topics and general interests"},
		}

		// 3. Insert into database
		if err := db.Create(&categories).Error; err != nil {
			fmt.Println("Error seeding categories:", err)
		} else {
			fmt.Println("Categories seeded successfully!")
		}
	} else {
		fmt.Println("Categories table already has data, skipping seed.")
	}
}
