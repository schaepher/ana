module example.com/fixtureapp

go 1.21

require (
	gorm.io/gorm v0.0.0
	xorm.io/xorm v0.0.0
)

replace (
	gorm.io/gorm => ./gorm
	xorm.io/xorm => ./xorm
)
