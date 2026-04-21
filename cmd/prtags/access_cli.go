package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/dutifuldev/prtags/internal/auth"
	"github.com/dutifuldev/prtags/internal/config"
	"github.com/dutifuldev/prtags/internal/core"
	"github.com/dutifuldev/prtags/internal/database"
	ghreplica "github.com/dutifuldev/prtags/internal/ghreplica"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/spf13/cobra"
)

func newAccessCommand() *cobra.Command {
	access := &cobra.Command{
		Use:   "access",
		Short: "Manage repository access grants",
	}
	access.AddCommand(newAccessGrantCommand())
	return access
}

func newAccessGrantCommand() *cobra.Command {
	grants := &cobra.Command{
		Use:   "grant",
		Short: "Manage repository access grants",
	}

	var repo string

	upsert := &cobra.Command{
		Use:   "upsert",
		Short: "Create or update a repository access grant",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			subject, err := resolveGrantSubject(cmd)
			if err != nil {
				return err
			}
			service, cleanup, err := openOpsService()
			if err != nil {
				return err
			}
			defer cleanup()

			grant, err := service.UpsertRepositoryAccessGrant(cmd.Context(), owner, name, core.RepositoryAccessGrantInput{
				GitHubUserID:          subject.userID,
				GitHubLogin:           subject.login,
				Role:                  mustFlag(cmd, "role"),
				GrantedByGitHubUserID: subject.grantedByUserID,
				GrantedByGitHubLogin:  subject.grantedByLogin,
			})
			if err != nil {
				return err
			}
			return printJSendSuccess(cmd.OutOrStdout(), grant)
		},
	}
	upsert.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = upsert.MarkFlagRequired("repo")
	upsert.Flags().Int64("github-user-id", 0, "GitHub user ID to grant")
	upsert.Flags().String("github-login", "", "GitHub login to grant")
	upsert.Flags().String("role", "writer", "grant role")
	upsert.Flags().Int64("granted-by-github-user-id", 0, "GitHub user ID that granted this access")
	upsert.Flags().String("granted-by-github-login", "", "GitHub login that granted this access")
	upsert.Flags().Bool("self", false, "use the stored prtags GitHub login as both the subject and granter")

	list := &cobra.Command{
		Use:   "list",
		Short: "List repository access grants",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			service, cleanup, err := openOpsService()
			if err != nil {
				return err
			}
			defer cleanup()

			grants, err := service.ListRepositoryAccessGrants(cmd.Context(), owner, name)
			if err != nil {
				return err
			}
			return printJSendSuccess(cmd.OutOrStdout(), grants)
		},
	}
	list.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = list.MarkFlagRequired("repo")

	revoke := &cobra.Command{
		Use:   "revoke",
		Short: "Delete a repository access grant",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			subject, err := resolveGrantSubject(cmd)
			if err != nil {
				return err
			}
			service, cleanup, err := openOpsService()
			if err != nil {
				return err
			}
			defer cleanup()

			if err := service.DeleteRepositoryAccessGrant(cmd.Context(), owner, name, subject.userID); err != nil {
				return err
			}
			return printJSendSuccess(cmd.OutOrStdout(), map[string]any{
				"revoked":            true,
				"github_repository":  strings.TrimSpace(repo),
				"github_user_id":     subject.userID,
				"github_login":       subject.login,
				"granted_by_user_id": subject.grantedByUserID,
				"granted_by_login":   subject.grantedByLogin,
			})
		},
	}
	revoke.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = revoke.MarkFlagRequired("repo")
	revoke.Flags().Int64("github-user-id", 0, "GitHub user ID to revoke")
	revoke.Flags().String("github-login", "", "GitHub login to revoke")
	revoke.Flags().Int64("granted-by-github-user-id", 0, "GitHub user ID that granted this access")
	revoke.Flags().String("granted-by-github-login", "", "GitHub login that granted this access")
	revoke.Flags().Bool("self", false, "use the stored prtags GitHub login as both the subject and granter")

	grants.AddCommand(upsert, list, revoke)
	return grants
}

type grantSubject struct {
	userID          int64
	login           string
	grantedByUserID int64
	grantedByLogin  string
}

func resolveGrantSubject(cmd *cobra.Command) (grantSubject, error) {
	if mustBoolFlag(cmd, "self") {
		token, err := loadStoredGrantor()
		if err != nil {
			return grantSubject{}, err
		}
		return grantSubject{
			userID:          token.UserID,
			login:           strings.TrimSpace(token.UserLogin),
			grantedByUserID: token.UserID,
			grantedByLogin:  strings.TrimSpace(token.UserLogin),
		}, nil
	}

	userID := int64(mustIntFlag64(cmd, "github-user-id"))
	login := strings.TrimSpace(mustFlag(cmd, "github-login"))
	if userID <= 0 {
		return grantSubject{}, fmt.Errorf("github-user-id is required")
	}
	if login == "" {
		return grantSubject{}, fmt.Errorf("github-login is required")
	}

	grantedByUserID := int64(mustIntFlag64(cmd, "granted-by-github-user-id"))
	grantedByLogin := strings.TrimSpace(mustFlag(cmd, "granted-by-github-login"))
	if grantedByUserID == 0 || grantedByLogin == "" {
		token, err := loadStoredGrantor()
		if err != nil {
			return grantSubject{}, fmt.Errorf("grantor identity is required; pass --granted-by-github-user-id and --granted-by-github-login, or run prtags auth login: %w", err)
		}
		if grantedByUserID == 0 {
			grantedByUserID = token.UserID
		}
		if grantedByLogin == "" {
			grantedByLogin = strings.TrimSpace(token.UserLogin)
		}
	}

	return grantSubject{
		userID:          userID,
		login:           login,
		grantedByUserID: grantedByUserID,
		grantedByLogin:  grantedByLogin,
	}, nil
}

func loadStoredGrantor() (auth.StoredToken, error) {
	token, err := auth.LoadStoredToken()
	if err != nil {
		return auth.StoredToken{}, err
	}
	if token.UserID <= 0 || strings.TrimSpace(token.UserLogin) == "" {
		return auth.StoredToken{}, fmt.Errorf("stored auth token is missing user identity")
	}
	return token, nil
}

func openOpsService() (*core.Service, func(), error) {
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}

	db, err := database.OpenWithPool(cfg.DatabaseURL, database.PoolConfig{
		MaxOpenConns:    cfg.DBMaxOpenConns,
		MaxIdleConns:    cfg.DBMaxIdleConns,
		ConnMaxIdleTime: cfg.DBConnMaxIdleTime,
		ConnMaxLifetime: cfg.DBConnMaxLifetime,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := database.RunMigrations(db); err != nil {
		return nil, nil, err
	}
	if err := database.EnsureGroupPublicIDs(context.Background(), db); err != nil {
		return nil, nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		_ = sqlDB.Close()
	}
	service := core.NewService(db, ghreplica.NewClient(cfg.GHReplicaBaseURL), permissions.AllowAllChecker{}, nil)
	return service, cleanup, nil
}
