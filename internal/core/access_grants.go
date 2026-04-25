package core

import (
	"context"
	"strings"

	"github.com/dutifuldev/prtags/internal/database"
)

const repositoryAccessGrantRoleWriter = "writer"

type RepositoryAccessGrantInput struct {
	GitHubUserID          int64
	GitHubLogin           string
	Role                  string
	GrantedByGitHubUserID int64
	GrantedByGitHubLogin  string
}

func (s *Service) UpsertRepositoryAccessGrant(ctx context.Context, owner, repo string, input RepositoryAccessGrantInput) (database.RepositoryAccessGrant, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return database.RepositoryAccessGrant{}, err
	}
	if err := validateRepositoryAccessGrantInput(input); err != nil {
		return database.RepositoryAccessGrant{}, err
	}

	role := normalizeRepositoryAccessGrantRole(input.Role)
	model := database.RepositoryAccessGrant{
		GitHubRepositoryID:    repository.GitHubRepositoryID,
		GitHubUserID:          input.GitHubUserID,
		GitHubLogin:           strings.TrimSpace(input.GitHubLogin),
		Role:                  role,
		GrantedByGitHubUserID: input.GrantedByGitHubUserID,
		GrantedByGitHubLogin:  strings.TrimSpace(input.GrantedByGitHubLogin),
	}
	if err := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND github_user_id = ?", repository.GitHubRepositoryID, input.GitHubUserID).
		Assign(model).
		FirstOrCreate(&model).Error; err != nil {
		return database.RepositoryAccessGrant{}, err
	}
	return model, nil
}

func (s *Service) ListRepositoryAccessGrants(ctx context.Context, owner, repo string) ([]database.RepositoryAccessGrant, error) {
	repository, err := s.readRepositoryProjection(ctx, owner, repo)
	if err != nil {
		return nil, err
	}

	var grants []database.RepositoryAccessGrant
	if err := s.db.WithContext(ctx).
		Where("github_repository_id = ?", repository.GitHubRepositoryID).
		Order("github_login ASC, github_user_id ASC").
		Find(&grants).Error; err != nil {
		return nil, err
	}
	return grants, nil
}

func (s *Service) DeleteRepositoryAccessGrant(ctx context.Context, owner, repo string, githubUserID int64) error {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return err
	}
	if githubUserID <= 0 {
		return &FailError{StatusCode: 400, Message: "github_user_id is required"}
	}

	result := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND github_user_id = ?", repository.GitHubRepositoryID, githubUserID).
		Delete(&database.RepositoryAccessGrant{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) hasRepositoryWriteGrant(ctx context.Context, githubRepositoryID, githubUserID int64) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&database.RepositoryAccessGrant{}).
		Where("github_repository_id = ? AND github_user_id = ? AND role = ?", githubRepositoryID, githubUserID, repositoryAccessGrantRoleWriter).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func validateRepositoryAccessGrantInput(input RepositoryAccessGrantInput) error {
	if input.GitHubUserID <= 0 {
		return &FailError{StatusCode: 400, Message: "github_user_id is required"}
	}
	if strings.TrimSpace(input.GitHubLogin) == "" {
		return &FailError{StatusCode: 400, Message: "github_login is required"}
	}
	if normalizeRepositoryAccessGrantRole(input.Role) != repositoryAccessGrantRoleWriter {
		return &FailError{StatusCode: 400, Message: "invalid repository access grant role"}
	}
	if input.GrantedByGitHubUserID <= 0 {
		return &FailError{StatusCode: 400, Message: "granted_by_github_user_id is required"}
	}
	if strings.TrimSpace(input.GrantedByGitHubLogin) == "" {
		return &FailError{StatusCode: 400, Message: "granted_by_github_login is required"}
	}
	return nil
}

func normalizeRepositoryAccessGrantRole(role string) string {
	if strings.TrimSpace(role) == "" {
		return repositoryAccessGrantRoleWriter
	}
	return strings.ToLower(strings.TrimSpace(role))
}
