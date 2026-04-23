package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dutifuldev/prtags/internal/publicid"
	"gorm.io/gorm"
)

func EnsureGroupPublicIDs(ctx context.Context, db *gorm.DB) error {
	groups, err := groupsMissingPublicIDs(ctx, db)
	if err != nil {
		return err
	}

	for _, group := range groups {
		if err := ensureGroupPublicID(ctx, db, &group); err != nil {
			return err
		}
	}

	return nil
}

func groupsMissingPublicIDs(ctx context.Context, db *gorm.DB) ([]Group, error) {
	var groups []Group
	err := db.WithContext(ctx).
		Where("public_id IS NULL OR public_id = ''").
		Order("id ASC").
		Find(&groups).Error
	return groups, err
}

func ensureGroupPublicID(ctx context.Context, db *gorm.DB, group *Group) error {
	oldTargetKey := legacyGroupTargetKey(group.ID)
	for attempts := 0; attempts < 20; attempts++ {
		publicID, err := publicid.NewGroupID()
		if err != nil {
			return err
		}
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return assignGroupPublicIDTx(tx, group.ID, oldTargetKey, publicID)
		})
		if err == nil {
			return refreshGroupPublicID(ctx, db, group)
		}
		if !isPublicIDConflict(err) || attempts == 19 {
			return err
		}
	}
	return nil
}

func assignGroupPublicIDTx(tx *gorm.DB, groupID uint, oldTargetKey, publicID string) error {
	result := tx.Model(&Group{}).
		Where("id = ? AND (public_id IS NULL OR public_id = '')", groupID).
		Update("public_id", publicID)
	if result.Error != nil || result.RowsAffected == 0 {
		return result.Error
	}
	return updateLegacyGroupTargetKeysTx(tx, oldTargetKey, groupTargetKey(publicID))
}

func updateLegacyGroupTargetKeysTx(tx *gorm.DB, oldTargetKey, newTargetKey string) error {
	for _, update := range legacyGroupTargetKeyUpdaters(oldTargetKey, newTargetKey) {
		if err := update(tx); err != nil {
			return err
		}
	}
	return nil
}

func legacyGroupTargetKeyUpdaters(oldTargetKey, newTargetKey string) []func(*gorm.DB) error {
	return []func(*gorm.DB) error{
		func(inner *gorm.DB) error {
			return inner.Model(&FieldValue{}).
				Where("target_type = ? AND target_key = ?", "group", oldTargetKey).
				Update("target_key", newTargetKey).Error
		},
		func(inner *gorm.DB) error {
			return inner.Model(&Event{}).
				Where("aggregate_type = ? AND aggregate_key = ?", "group", oldTargetKey).
				Update("aggregate_key", newTargetKey).Error
		},
		func(inner *gorm.DB) error {
			return inner.Model(&EventRef{}).
				Where("ref_type = ? AND ref_key = ?", "group", oldTargetKey).
				Update("ref_key", newTargetKey).Error
		},
		func(inner *gorm.DB) error {
			return inner.Model(&SearchDocument{}).
				Where("target_type = ? AND target_key = ?", "group", oldTargetKey).
				Update("target_key", newTargetKey).Error
		},
		func(inner *gorm.DB) error {
			return inner.Model(&Embedding{}).
				Where("target_type = ? AND target_key = ?", "group", oldTargetKey).
				Update("target_key", newTargetKey).Error
		},
		func(inner *gorm.DB) error {
			return inner.Model(&IndexJob{}).
				Where("target_type = ? AND target_key = ?", "group", oldTargetKey).
				Update("target_key", newTargetKey).Error
		},
	}
}

func refreshGroupPublicID(ctx context.Context, db *gorm.DB, group *Group) error {
	var refreshed Group
	if err := db.WithContext(ctx).Select("public_id").First(&refreshed, group.ID).Error; err != nil {
		return err
	}
	group.PublicID = refreshed.PublicID
	return nil
}

func legacyGroupTargetKey(groupID uint) string {
	return fmt.Sprintf("group:%d", groupID)
}

func groupTargetKey(publicID string) string {
	return "group:" + publicID
}

func isPublicIDConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "idx_groups_public_id") ||
		strings.Contains(text, "groups.public_id") ||
		strings.Contains(text, "duplicate key")
}
