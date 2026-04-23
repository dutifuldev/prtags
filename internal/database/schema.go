package database

import (
	"errors"

	"gorm.io/gorm"
)

var ErrAutoMigrateDisabled = errors.New("database.AutoMigrate is disabled; use SQL migrations in runtime code and ApplyTestSchema for SQLite tests")

func schemaModels() []any {
	return []any{
		&RepositoryProjection{},
		&RepositoryAccessGrant{},
		&TargetProjection{},
		&Group{},
		&GroupMember{},
		&FieldDefinition{},
		&FieldValue{},
		&Event{},
		&EventRef{},
		&SearchDocument{},
		&Embedding{},
		&IndexJob{},
		&GroupCommentSyncTarget{},
	}
}

func ApplyTestSchema(db *gorm.DB) error {
	if db == nil {
		return errors.New("database.ApplyTestSchema requires a database handle")
	}
	dialector := db.Dialector
	if dialector == nil || dialector.Name() != "sqlite" {
		return errors.New("database.ApplyTestSchema only supports sqlite test databases")
	}
	return db.AutoMigrate(schemaModels()...)
}

func AutoMigrate(_ *gorm.DB) error {
	return ErrAutoMigrateDisabled
}
