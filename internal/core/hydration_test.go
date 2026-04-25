package core

import (
	"context"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestBatchAnnotationHydrationHelpers(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "intent",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)
	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{
		"intent": "batch annotations",
	}, "")
	require.NoError(t, err)

	require.Empty(t, annotationTargetsForSearchRows([]scoredSearchTarget{{TargetType: "pull_request"}}))
	require.Empty(t, annotationTargetsForFieldValues([]database.FieldValue{{TargetType: "pull_request"}}))

	targetKey := objectTargetKey(101, "pull_request", 22)
	empty, err := service.getAnnotationsForTargetKeys(ctx, 101, []annotationTarget{{}})
	require.NoError(t, err)
	require.Empty(t, empty)

	annotations, err := service.getAnnotationsForTargetKeys(ctx, 101, []annotationTarget{
		{targetType: "pull_request", targetKey: targetKey},
		{targetType: "pull_request", targetKey: targetKey},
		{targetType: "issue", targetKey: objectTargetKey(101, "issue", 11)},
		{},
	})
	require.NoError(t, err)
	require.Equal(t, "batch annotations", annotations[annotationMapKey("pull_request", targetKey)]["intent"])
	require.Empty(t, annotations[annotationMapKey("issue", objectTargetKey(101, "issue", 11))])
}

func TestSearchHydrationHelpersCoverInvalidAndGroupTargets(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	group, err := service.CreateGroup(ctx, permissionsActor(), "acme", "widgets", GroupInput{Kind: "mixed", Title: "Auth"}, "")
	require.NoError(t, err)

	targetKey := objectTargetKey(101, "pull_request", 22)
	annotations := map[string]map[string]any{
		annotationMapKey("pull_request", targetKey):               {"intent": "retry"},
		annotationMapKey("group", groupTargetKey(group.PublicID)): {"theme": "auth"},
	}
	summaries := map[string]GroupMemberObjectSummary{
		targetKey: {Title: "Retry auth", State: "open", UpdatedAt: time.Now().UTC()},
	}

	objectResult, err := service.populateObjectSearchResultWithHydration("pull_request", targetKey, TextSearchResult{TargetType: "pull_request", TargetKey: targetKey}, summaries, annotations)
	require.NoError(t, err)
	require.Equal(t, "Retry auth", objectResult.ObjectSummary.Title)
	require.Equal(t, "retry", objectResult.Annotations["intent"])

	invalidObject, err := service.populateObjectSearchResultWithHydration("pull_request", "bad-key", TextSearchResult{TargetKey: "bad-key"}, summaries, annotations)
	require.NoError(t, err)
	require.Nil(t, invalidObject.Annotations)

	groupResult, err := service.populateGroupSearchResultWithAnnotations(ctx, groupTargetKey(group.PublicID), TextSearchResult{TargetType: "group", TargetKey: groupTargetKey(group.PublicID)}, annotations)
	require.NoError(t, err)
	require.Equal(t, group.PublicID, groupResult.ID)
	require.Equal(t, "auth", groupResult.Annotations["theme"])

	require.Equal(t, []string{group.PublicID}, groupPublicIDsForSearchRows([]scoredSearchTarget{
		{TargetType: "group", TargetKey: groupTargetKey(group.PublicID)},
		{TargetType: "group", TargetKey: groupTargetKey(group.PublicID)},
		{TargetType: "pull_request", TargetKey: objectTargetKey(101, "pull_request", 22)},
		{TargetType: "group", TargetKey: "bad-key"},
	}))
	emptyGroups, err := service.groupsByPublicID(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, emptyGroups)
	groups, err := service.groupsByPublicID(ctx, []string{group.PublicID})
	require.NoError(t, err)
	require.Equal(t, group.PublicID, groups[group.PublicID].PublicID)
	groupHydrated, err := service.populateGroupSearchResultWithHydration(groupTargetKey(group.PublicID), TextSearchResult{TargetType: "group", TargetKey: groupTargetKey(group.PublicID)}, annotations, groups)
	require.NoError(t, err)
	require.Equal(t, group.PublicID, groupHydrated.ID)
	require.Equal(t, "auth", groupHydrated.Annotations["theme"])

	_, err = service.populateGroupSearchResultWithHydration(groupTargetKey("missing"), TextSearchResult{TargetType: "group", TargetKey: groupTargetKey("missing")}, annotations, groups)
	require.ErrorIs(t, err, ErrNotFound)

	invalidGroup, err := service.populateGroupSearchResultWithAnnotations(ctx, "bad-key", TextSearchResult{TargetKey: "bad-key"}, annotations)
	require.NoError(t, err)
	require.Empty(t, invalidGroup.ID)

	_, err = service.populateGroupSearchResultWithAnnotations(ctx, groupTargetKey("missing"), TextSearchResult{TargetType: "group", TargetKey: groupTargetKey("missing")}, annotations)
	require.Error(t, err)
}

func TestFilterHydrationHelpersCoverBatchBranches(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	group, err := service.CreateGroup(ctx, permissionsActor(), "acme", "widgets", GroupInput{Kind: "mixed", Title: "Auth"}, "")
	require.NoError(t, err)

	values := []database.FieldValue{
		{
			GitHubRepositoryID: 101,
			TargetType:         "pull_request",
			ObjectNumber:       intPtr(22),
			TargetKey:          objectTargetKey(101, "pull_request", 22),
			MultiEnumJSON:      datatypes.JSON(`["bug","auth"]`),
		},
		{
			GitHubRepositoryID: 101,
			TargetType:         "issue",
			ObjectNumber:       intPtr(11),
			TargetKey:          objectTargetKey(101, "issue", 11),
			MultiEnumJSON:      datatypes.JSON(`["docs"]`),
		},
	}

	nonMulti, err := matchingFieldValues("text", "auth", values)
	require.NoError(t, err)
	require.Len(t, nonMulti, 2)
	matched, err := matchingFieldValues("multi_enum", "auth", values)
	require.NoError(t, err)
	require.Len(t, matched, 1)
	_, err = matchingFieldValues("multi_enum", "auth", []database.FieldValue{{MultiEnumJSON: datatypes.JSON(`{`)}})
	require.Error(t, err)

	emptySummaries, err := service.filteredTargetSummaries(ctx, 101, []database.FieldValue{{TargetType: "group"}})
	require.NoError(t, err)
	require.Empty(t, emptySummaries)
	summaries, err := service.filteredTargetSummaries(ctx, 101, values)
	require.NoError(t, err)
	require.Equal(t, "Retry ACP turns safely", summaries[objectTargetKey(101, "pull_request", 22)].Title)

	require.Empty(t, groupIDsForFieldValues(values))
	groupValue := database.FieldValue{
		GitHubRepositoryID: 101,
		TargetType:         "group",
		TargetKey:          groupTargetKey(group.PublicID),
		GroupID:            &group.ID,
	}
	require.Equal(t, []uint{group.ID}, groupIDsForFieldValues([]database.FieldValue{groupValue, groupValue}))

	emptyGroups, err := service.groupsByID(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, emptyGroups)
	groups, err := service.groupsByID(ctx, []uint{group.ID})
	require.NoError(t, err)
	require.Equal(t, group.PublicID, groups[group.ID].PublicID)

	annotations := map[string]map[string]any{
		annotationMapKey("pull_request", objectTargetKey(101, "pull_request", 22)): {"intent": "retry"},
		annotationMapKey("group", groupTargetKey(group.PublicID)):                  {"theme": "auth"},
	}
	objectResult, err := filteredTargetResultFromHydration(values[0], summaries, annotations, groups)
	require.NoError(t, err)
	require.Equal(t, 22, objectResult.ObjectNumber)
	require.Equal(t, "retry", objectResult.Annotations["intent"])
	require.Equal(t, "Retry ACP turns safely", objectResult.ObjectSummary.Title)

	groupResult, err := filteredTargetResultFromHydration(groupValue, summaries, annotations, groups)
	require.NoError(t, err)
	require.Equal(t, group.PublicID, groupResult.ID)
	require.Equal(t, "auth", groupResult.Annotations["theme"])

	groupMissing := groupValue
	missingID := group.ID + 999
	groupMissing.GroupID = &missingID
	_, err = filteredTargetResultFromHydration(groupMissing, summaries, nil, groups)
	require.ErrorIs(t, err, ErrNotFound)

	noAnnotations, err := filteredTargetResultFromHydration(values[1], summaries, nil, groups)
	require.NoError(t, err)
	require.Empty(t, noAnnotations.Annotations)
}
