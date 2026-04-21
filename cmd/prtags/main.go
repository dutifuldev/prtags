package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dutifuldev/prtags/internal/auth"
	"github.com/dutifuldev/prtags/internal/cli"
	"github.com/dutifuldev/prtags/internal/config"
	"github.com/dutifuldev/prtags/internal/core"
	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	ghreplica "github.com/dutifuldev/prtags/internal/ghreplica"
	"github.com/dutifuldev/prtags/internal/githubapi"
	"github.com/dutifuldev/prtags/internal/httpapi"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func main() {
	root := newRootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var serverURL string

	root := &cobra.Command{
		Use: "prtags",
	}
	root.PersistentFlags().StringVar(&serverURL, "server", "http://127.0.0.1:8081", "PRtags API base URL")

	root.AddCommand(newAuthCommand())
	root.AddCommand(newServeCommand())
	root.AddCommand(newWorkerCommand())
	root.AddCommand(newFieldCommand(&serverURL))
	root.AddCommand(newGroupCommand(&serverURL))
	root.AddCommand(newAnnotationCommand(&serverURL))
	root.AddCommand(newSearchCommand(&serverURL))
	root.AddCommand(newTargetsCommand(&serverURL))
	root.AddCommand(newAccessCommand())

	return root
}

func newAuthCommand() *cobra.Command {
	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage PRtags GitHub authentication",
	}

	var clientID string
	var scope string
	login := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with GitHub using device flow",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := auth.DefaultConfig()
			if cmd.Flags().Changed("client-id") {
				cfg.ClientID = mustFlag(cmd, "client-id")
			}
			if cmd.Flags().Changed("scope") {
				cfg.Scope = mustFlag(cmd, "scope")
			}
			if strings.TrimSpace(cfg.ClientID) == "" {
				return fmt.Errorf("github oauth client id is required")
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			device, err := cfg.StartDeviceFlow(ctx)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Open %s and enter code %s\n", device.VerificationURI, device.UserCode)
			fmt.Fprintln(cmd.OutOrStdout(), "Waiting for GitHub authorization...")

			token, err := cfg.PollAccessToken(
				ctx,
				device.DeviceCode,
				time.Duration(device.Interval)*time.Second,
				time.Duration(device.ExpiresIn)*time.Second,
			)
			if err != nil {
				return err
			}

			viewer, err := cfg.GetViewer(ctx, token.AccessToken)
			if err != nil {
				return err
			}

			path, err := auth.SaveStoredToken(auth.StoredToken{
				ClientID:     cfg.ClientID,
				OAuthBaseURL: cfg.OAuthBaseURL,
				APIBaseURL:   cfg.APIBaseURL,
				AccessToken:  token.AccessToken,
				TokenType:    token.TokenType,
				Scope:        token.Scope,
				UserLogin:    viewer.Login,
				UserID:       viewer.ID,
			})
			if err != nil {
				return err
			}

			scopes := strings.TrimSpace(token.Scope)
			if scopes == "" {
				scopes = cfg.Scope
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s\n", viewer.Login)
			fmt.Fprintf(cmd.OutOrStdout(), "Scopes: %s\n", scopes)
			fmt.Fprintf(cmd.OutOrStdout(), "Saved token to %s\n", path)
			return nil
		},
	}
	login.Flags().StringVar(&clientID, "client-id", auth.DefaultConfig().ClientID, "GitHub OAuth client ID")
	login.Flags().StringVar(&scope, "scope", auth.DefaultScope, "space-delimited GitHub OAuth scopes")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show stored GitHub authentication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := auth.LoadStoredToken()
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "Not logged in.")
					return nil
				}
				return err
			}
			path, err := auth.StoredTokenPath()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s\n", token.UserLogin)
			fmt.Fprintf(cmd.OutOrStdout(), "Scopes: %s\n", token.Scope)
			fmt.Fprintf(cmd.OutOrStdout(), "Token file: %s\n", path)
			return nil
		},
	}

	logout := &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored GitHub authentication token",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := auth.StoredTokenPath()
			if err != nil {
				return err
			}
			if err := auth.DeleteStoredToken(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed stored token at %s\n", path)
			return nil
		},
	}

	authCmd.AddCommand(login, status, logout)
	return authCmd
}

func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the PRtags HTTP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.FromEnv()
			if err := cfg.Validate(); err != nil {
				return err
			}

			db, err := database.Open(cfg.DatabaseURL)
			if err != nil {
				return err
			}
			if err := database.RunMigrations(db); err != nil {
				return err
			}
			if err := database.EnsureGroupPublicIDs(context.Background(), db); err != nil {
				return err
			}

			checker := permissions.Checker(permissions.AllowAllChecker{})
			if !cfg.AllowUnauthWrites {
				checker = permissions.NewGitHubChecker(0)
			}

			ghClient := ghreplica.NewClient(cfg.GHReplicaBaseURL)
			indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider(cfg.EmbeddingModel, database.EmbeddingDimensions))
			service := core.NewService(db, ghClient, checker, indexer)
			var commentSync *core.CommentSyncService
			if cfg.HasGitHubApp() {
				commentSync = core.NewCommentSyncService(db, githubapi.NewClient(cfg.GitHubBaseURL, githubapi.AuthConfig{
					AppID:          cfg.GitHubAppID,
					InstallationID: cfg.GitHubInstallationID,
					PrivateKeyPEM:  cfg.GitHubAppPrivateKeyPEM,
					PrivateKeyPath: cfg.GitHubAppPrivateKeyPath,
				}), nil)
			}
			sqlDB, err := db.DB()
			if err != nil {
				return err
			}
			dispatcher, err := core.NewRiverDispatcher(sqlDB, indexer, commentSync)
			if err != nil {
				return err
			}
			service.SetJobDispatcher(dispatcher)
			service.SetCommentSync(commentSync)
			if commentSync != nil {
				commentSync.SetDispatcher(dispatcher)
			}
			server := httpapi.NewServer(db, service, cfg.AllowUnauthWrites)

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if err := dispatcher.ImportLegacyIndexJobs(ctx, db); err != nil {
				return err
			}
			if cfg.EnableWorker {
				if err := dispatcher.Start(ctx); err != nil {
					return err
				}
			}
			go func() {
				<-ctx.Done()
				_ = dispatcher.Stop(context.Background())
				_ = server.Echo().Shutdown(context.Background())
			}()
			return server.Echo().Start(cfg.ListenAddr)
		},
	}
}

func newWorkerCommand() *cobra.Command {
	worker := &cobra.Command{
		Use:   "worker",
		Short: "Run River workers",
	}
	worker.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Run background workers",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.FromEnv()
			if err := cfg.Validate(); err != nil {
				return err
			}
			db, err := database.Open(cfg.DatabaseURL)
			if err != nil {
				return err
			}
			if err := database.RunMigrations(db); err != nil {
				return err
			}
			if err := database.EnsureGroupPublicIDs(context.Background(), db); err != nil {
				return err
			}
			indexer := core.NewIndexer(db, ghreplica.NewClient(cfg.GHReplicaBaseURL), embedding.NewLocalHashProvider(cfg.EmbeddingModel, database.EmbeddingDimensions))
			var commentSync *core.CommentSyncService
			if cfg.HasGitHubApp() {
				commentSync = core.NewCommentSyncService(db, githubapi.NewClient(cfg.GitHubBaseURL, githubapi.AuthConfig{
					AppID:          cfg.GitHubAppID,
					InstallationID: cfg.GitHubInstallationID,
					PrivateKeyPEM:  cfg.GitHubAppPrivateKeyPEM,
					PrivateKeyPath: cfg.GitHubAppPrivateKeyPath,
				}), nil)
			}
			sqlDB, err := db.DB()
			if err != nil {
				return err
			}
			dispatcher, err := core.NewRiverDispatcher(sqlDB, indexer, commentSync)
			if err != nil {
				return err
			}
			if commentSync != nil {
				commentSync.SetDispatcher(dispatcher)
			}
			if err := dispatcher.ImportLegacyIndexJobs(cmd.Context(), db); err != nil {
				return err
			}
			if err := dispatcher.Start(cmd.Context()); err != nil {
				return err
			}
			<-cmd.Context().Done()
			return dispatcher.Stop(context.Background())
		},
	})
	return worker
}

func newFieldCommand(serverURL *string) *cobra.Command {
	fields := &cobra.Command{Use: "field"}

	var repo string
	create := &cobra.Command{
		Use: "create",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			payload := parseDesiredFieldDefinition(cobraFlagReader{cmd: cmd}).createPayload()
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/fields", owner, name), payload)
		},
	}
	create.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = create.MarkFlagRequired("repo")
	create.Flags().String("name", "", "field name")
	create.Flags().String("display-name", "", "display name")
	create.Flags().String("scope", "", "field scope")
	create.Flags().String("type", "", "field type")
	create.Flags().String("enum-values", "", "comma-separated enum values")
	create.Flags().Bool("required", false, "field is required")
	create.Flags().Bool("filterable", false, "field is filterable")
	create.Flags().Bool("searchable", false, "field is searchable")
	create.Flags().Bool("vectorized", false, "field is vectorized")
	create.Flags().Int("sort-order", 0, "field sort order")
	_ = create.MarkFlagRequired("name")
	_ = create.MarkFlagRequired("scope")
	_ = create.MarkFlagRequired("type")

	ensure := &cobra.Command{
		Use:   "ensure",
		Short: "Create a field if missing, or update it to the requested shape",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			desired := parseDesiredFieldDefinition(cobraFlagReader{cmd: cmd})
			fields, err := fetchFieldDefinitions(context.Background(), *serverURL, owner, name)
			if err != nil {
				return err
			}

			var existing *fieldDefinitionView
			for i := range fields {
				if fields[i].Name == desired.Name && fields[i].ObjectScope == desired.ObjectScope {
					existing = &fields[i]
					break
				}
			}

			if existing == nil {
				created, err := createFieldDefinition(context.Background(), *serverURL, owner, name, desired.createPayload())
				if err != nil {
					return err
				}
				return printJSendSuccess(cmd.OutOrStdout(), fieldEnsureView{fieldDefinitionView: created, Action: "created"})
			}
			if existing.ArchivedAt != nil {
				return fmt.Errorf("field %q (%s) exists but is archived", existing.Name, existing.ObjectScope)
			}
			if existing.FieldType != desired.FieldType {
				return fmt.Errorf("field %q (%s) already exists with type %q", existing.Name, existing.ObjectScope, existing.FieldType)
			}

			patch := diffFieldDefinition(*existing, desired)
			if len(patch) == 0 {
				return printJSendSuccess(cmd.OutOrStdout(), fieldEnsureView{fieldDefinitionView: *existing, Action: "noop"})
			}

			updated, err := updateFieldDefinition(context.Background(), *serverURL, owner, name, existing.ID, patch)
			if err != nil {
				return err
			}
			return printJSendSuccess(cmd.OutOrStdout(), fieldEnsureView{fieldDefinitionView: updated, Action: "updated"})
		},
	}
	ensure.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = ensure.MarkFlagRequired("repo")
	ensure.Flags().String("name", "", "field name")
	ensure.Flags().String("display-name", "", "display name")
	ensure.Flags().String("scope", "", "field scope")
	ensure.Flags().String("type", "", "field type")
	ensure.Flags().String("enum-values", "", "comma-separated enum values")
	ensure.Flags().Bool("required", false, "field is required")
	ensure.Flags().Bool("filterable", false, "field is filterable")
	ensure.Flags().Bool("searchable", false, "field is searchable")
	ensure.Flags().Bool("vectorized", false, "field is vectorized")
	ensure.Flags().Int("sort-order", 0, "field sort order")
	_ = ensure.MarkFlagRequired("name")
	_ = ensure.MarkFlagRequired("scope")
	_ = ensure.MarkFlagRequired("type")

	list := &cobra.Command{
		Use:   "list",
		Short: "List repo field definitions with optional filtering",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			fields, err := fetchFieldDefinitions(context.Background(), *serverURL, owner, name)
			if err != nil {
				return err
			}
			filtered := filterFieldDefinitions(fields, fieldListFilters{
				Name:        mustFlag(cmd, "name"),
				ObjectScope: mustFlag(cmd, "scope"),
				FieldType:   mustFlag(cmd, "type"),
				ActiveOnly:  mustBoolFlag(cmd, "active-only"),
			})
			format := mustFlag(cmd, "format")
			if format == "auto" {
				format = defaultFieldListFormat(cmd.OutOrStdout())
			}
			return printFieldDefinitions(cmd.OutOrStdout(), filtered, format)
		},
	}
	list.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = list.MarkFlagRequired("repo")
	list.Flags().String("scope", "", "only show fields for this scope")
	list.Flags().String("type", "", "only show fields of this type")
	list.Flags().String("name", "", "only show the field with this name")
	list.Flags().Bool("active-only", false, "only show active fields")
	list.Flags().String("format", "auto", "output format: auto, json, table")

	update := &cobra.Command{
		Use:  "update <field-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			payload := map[string]any{}
			if cmd.Flags().Changed("display-name") {
				payload["display_name"] = cmd.Flag("display-name").Value.String()
			}
			if cmd.Flags().Changed("enum-values") {
				payload["enum_values"] = strings.Fields(strings.ReplaceAll(cmd.Flag("enum-values").Value.String(), ",", " "))
			}
			if cmd.Flags().Changed("required") {
				payload["is_required"] = mustBoolFlag(cmd, "required")
			}
			if cmd.Flags().Changed("filterable") {
				payload["is_filterable"] = mustBoolFlag(cmd, "filterable")
			}
			if cmd.Flags().Changed("searchable") {
				payload["is_searchable"] = mustBoolFlag(cmd, "searchable")
			}
			if cmd.Flags().Changed("vectorized") {
				payload["is_vectorized"] = mustBoolFlag(cmd, "vectorized")
			}
			if cmd.Flags().Changed("sort-order") {
				payload["sort_order"] = mustIntFlag(cmd, "sort-order")
			}
			if cmd.Flags().Changed("row-version") {
				payload["expected_row_version"] = mustIntFlag(cmd, "row-version")
			}
			if len(payload) == 0 {
				return fmt.Errorf("at least one field update is required")
			}
			return doPrintJSON(context.Background(), *serverURL, "PATCH", fmt.Sprintf("/v1/repos/%s/%s/fields/%s", owner, name, args[0]), payload)
		},
	}
	update.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = update.MarkFlagRequired("repo")
	update.Flags().String("display-name", "", "new display name")
	update.Flags().String("enum-values", "", "comma-separated enum values")
	update.Flags().Bool("required", false, "field is required")
	update.Flags().Bool("filterable", false, "field is filterable")
	update.Flags().Bool("searchable", false, "field is searchable")
	update.Flags().Bool("vectorized", false, "field is vectorized")
	update.Flags().Int("sort-order", 0, "field sort order")
	update.Flags().Int("row-version", 0, "expected current row version")

	archive := &cobra.Command{
		Use:  "archive <field-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			var payload any
			if cmd.Flags().Changed("row-version") {
				payload = map[string]any{"expected_row_version": mustIntFlag(cmd, "row-version")}
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/fields/%s/archive", owner, name, args[0]), payload)
		},
	}
	archive.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = archive.MarkFlagRequired("repo")
	archive.Flags().Int("row-version", 0, "expected current row version")

	importCmd := &cobra.Command{
		Use:  "import <file>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			contents, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var manifest any
			if strings.HasSuffix(args[0], ".yaml") || strings.HasSuffix(args[0], ".yml") {
				if err := yaml.Unmarshal(contents, &manifest); err != nil {
					return err
				}
			} else {
				if err := json.Unmarshal(contents, &manifest); err != nil {
					return err
				}
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/fields/import", owner, name), manifest)
		},
	}
	importCmd.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = importCmd.MarkFlagRequired("repo")

	exportCmd := &cobra.Command{
		Use: "export",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			client := cli.NewClient(*serverURL)
			raw, err := client.DoJSON(context.Background(), "GET", fmt.Sprintf("/v1/repos/%s/%s/fields/export", owner, name), nil)
			if err != nil {
				return err
			}
			manifestRaw, err := cli.ExtractJSendData(raw)
			if err != nil {
				return err
			}
			if mustBoolFlag(cmd, "yaml") {
				var payload any
				if err := json.Unmarshal(manifestRaw, &payload); err != nil {
					return err
				}
				out, err := yaml.Marshal(payload)
				if err != nil {
					return err
				}
				fmt.Print(string(out))
				return nil
			}
			fmt.Println(prettyJSON(manifestRaw))
			return nil
		},
	}
	exportCmd.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = exportCmd.MarkFlagRequired("repo")
	exportCmd.Flags().Bool("yaml", false, "render YAML")

	fields.AddCommand(create, ensure, list, update, archive, importCmd, exportCmd)
	return fields
}

func newGroupCommand(serverURL *string) *cobra.Command {
	groups := &cobra.Command{Use: "group"}
	var repo string

	create := &cobra.Command{
		Use: "create",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"kind":        mustFlag(cmd, "kind"),
				"title":       mustFlag(cmd, "title"),
				"description": cmd.Flag("description").Value.String(),
				"status":      cmd.Flag("status").Value.String(),
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/groups", owner, name), payload)
		},
	}
	create.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = create.MarkFlagRequired("repo")
	create.Flags().String("kind", "", "group kind")
	create.Flags().String("title", "", "group title")
	create.Flags().String("description", "", "group description")
	create.Flags().String("status", "open", "group status")
	_ = create.MarkFlagRequired("kind")
	_ = create.MarkFlagRequired("title")

	list := &cobra.Command{
		Use: "list",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "GET", fmt.Sprintf("/v1/repos/%s/%s/groups", owner, name), nil)
		},
	}
	list.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = list.MarkFlagRequired("repo")

	get := &cobra.Command{
		Use:  "get <group-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/groups/" + args[0]
			includeMetadata, err := cmd.Flags().GetBool("include-metadata")
			if err != nil {
				return err
			}
			if includeMetadata {
				path += "?include=metadata"
			}
			return doPrintJSON(context.Background(), *serverURL, "GET", path, nil)
		},
	}
	get.Flags().Bool("include-metadata", false, "include cached object metadata for group members")

	update := &cobra.Command{
		Use:  "update <group-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := map[string]any{}
			if cmd.Flags().Changed("title") {
				payload["title"] = cmd.Flag("title").Value.String()
			}
			if cmd.Flags().Changed("description") {
				payload["description"] = cmd.Flag("description").Value.String()
			}
			if cmd.Flags().Changed("status") {
				payload["status"] = cmd.Flag("status").Value.String()
			}
			if cmd.Flags().Changed("row-version") {
				payload["expected_row_version"] = mustIntFlag(cmd, "row-version")
			}
			if len(payload) == 0 {
				return fmt.Errorf("at least one group update is required")
			}
			return doPrintJSON(context.Background(), *serverURL, "PATCH", "/v1/groups/"+args[0], payload)
		},
	}
	update.Flags().String("title", "", "new group title")
	update.Flags().String("description", "", "new group description")
	update.Flags().String("status", "", "new group status")
	update.Flags().Int("row-version", 0, "expected current row version")

	addPR := &cobra.Command{
		Use:  "add-pr <group-id> <pr-number>",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", "/v1/groups/"+args[0]+"/members", map[string]any{
				"object_type":   "pull_request",
				"object_number": number,
			})
		},
	}

	addIssue := &cobra.Command{
		Use:  "add-issue <group-id> <issue-number>",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", "/v1/groups/"+args[0]+"/members", map[string]any{
				"object_type":   "issue",
				"object_number": number,
			})
		},
	}

	syncComments := &cobra.Command{
		Use:  "sync-comments <group-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doPrintJSON(context.Background(), *serverURL, "POST", "/v1/groups/"+args[0]+"/sync-comments", nil)
		},
	}

	groups.AddCommand(create, list, get, update, addPR, addIssue, syncComments)
	return groups
}

func newAnnotationCommand(serverURL *string) *cobra.Command {
	root := &cobra.Command{Use: "annotation"}

	root.AddCommand(newObjectAnnotationCommand(serverURL, "pr", "pulls"))
	root.AddCommand(newObjectAnnotationCommand(serverURL, "issue", "issues"))

	group := &cobra.Command{Use: "group"}
	group.AddCommand(&cobra.Command{
		Use:  "set <group-id> <field=value> [field=value...]",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			annotations, err := parseAnnotationPairs(args[1:])
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", "/v1/groups/"+args[0]+"/annotations", annotations)
		},
	})
	group.AddCommand(&cobra.Command{
		Use:  "clear <group-id> <field> [field...]",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			annotations, err := parseAnnotationKeys(args[1:])
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", "/v1/groups/"+args[0]+"/annotations", annotations)
		},
	})
	group.AddCommand(&cobra.Command{
		Use:  "get <group-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doPrintJSON(context.Background(), *serverURL, "GET", "/v1/groups/"+args[0]+"/annotations", nil)
		},
	})

	root.AddCommand(group)
	return root
}

func newObjectAnnotationCommand(serverURL *string, name, route string) *cobra.Command {
	var repo string
	cmd := &cobra.Command{Use: name}
	set := &cobra.Command{
		Use:  "set <number> <field=value> [field=value...]",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repoName, err := splitRepo(repo)
			if err != nil {
				return err
			}
			annotations, err := parseAnnotationPairs(args[1:])
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/%s/%s/annotations", owner, repoName, route, args[0]), annotations)
		},
	}
	set.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = set.MarkFlagRequired("repo")
	clear := &cobra.Command{
		Use:  "clear <number> <field> [field...]",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repoName, err := splitRepo(repo)
			if err != nil {
				return err
			}
			annotations, err := parseAnnotationKeys(args[1:])
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/%s/%s/annotations", owner, repoName, route, args[0]), annotations)
		},
	}
	clear.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = clear.MarkFlagRequired("repo")
	get := &cobra.Command{
		Use:  "get <number>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repoName, err := splitRepo(repo)
			if err != nil {
				return err
			}
			return doPrintJSON(context.Background(), *serverURL, "GET", fmt.Sprintf("/v1/repos/%s/%s/%s/%s/annotations", owner, repoName, route, args[0]), nil)
		},
	}
	get.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = get.MarkFlagRequired("repo")
	cmd.AddCommand(set, clear, get)
	return cmd
}

func newSearchCommand(serverURL *string) *cobra.Command {
	search := &cobra.Command{Use: "search"}
	var repo string

	text := &cobra.Command{
		Use:  "text <query>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"query":        args[0],
				"target_types": strings.Fields(strings.ReplaceAll(cmd.Flag("types").Value.String(), ",", " ")),
				"limit":        mustIntFlag(cmd, "limit"),
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/search/text", owner, name), payload)
		},
	}
	text.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = text.MarkFlagRequired("repo")
	text.Flags().String("types", "pull_request issue group", "target types")
	text.Flags().Int("limit", 10, "result limit")

	similar := &cobra.Command{
		Use:  "similar <query>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"query":        args[0],
				"target_types": strings.Fields(strings.ReplaceAll(cmd.Flag("types").Value.String(), ",", " ")),
				"limit":        mustIntFlag(cmd, "limit"),
			}
			return doPrintJSON(context.Background(), *serverURL, "POST", fmt.Sprintf("/v1/repos/%s/%s/search/similar", owner, name), payload)
		},
	}
	similar.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = similar.MarkFlagRequired("repo")
	similar.Flags().String("types", "pull_request issue group", "target types")
	similar.Flags().Int("limit", 10, "result limit")

	search.AddCommand(text, similar)
	return search
}

func newTargetsCommand(serverURL *string) *cobra.Command {
	var repo string
	targets := &cobra.Command{Use: "targets"}
	filter := &cobra.Command{
		Use: "filter",
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := splitRepo(repo)
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/v1/repos/%s/%s/targets?target_type=%s&field=%s&value=%s", owner, name, mustFlag(cmd, "type"), mustFlag(cmd, "field"), mustFlag(cmd, "value"))
			return doPrintJSON(context.Background(), *serverURL, "GET", path, nil)
		},
	}
	filter.Flags().StringVarP(&repo, "repo", "R", "", "repo in owner/name form")
	_ = filter.MarkFlagRequired("repo")
	filter.Flags().String("type", "", "target type")
	filter.Flags().String("field", "", "field name")
	filter.Flags().String("value", "", "field value")
	_ = filter.MarkFlagRequired("type")
	_ = filter.MarkFlagRequired("field")
	_ = filter.MarkFlagRequired("value")
	targets.AddCommand(filter)
	return targets
}

func doPrintJSON(ctx context.Context, serverURL, method, path string, payload any) error {
	client := cli.NewClient(serverURL)
	raw, err := client.DoJSON(ctx, method, path, payload)
	if err != nil {
		return err
	}
	fmt.Println(prettyJSON(raw))
	return nil
}

func prettyJSON(raw []byte) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func splitRepo(fullName string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(fullName), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo must be owner/name")
	}
	return parts[0], parts[1], nil
}

func parseAnnotationPairs(pairs []string) (map[string]any, error) {
	if len(pairs) == 0 {
		return nil, fmt.Errorf("at least one annotation is required")
	}
	out := map[string]any{}
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid annotation %q", pair)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("annotation key is required")
		}
		if strings.EqualFold(value, "null") {
			out[key] = nil
			continue
		}
		if value == "true" || value == "false" {
			out[key] = value == "true"
			continue
		}
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			var items []any
			if err := json.Unmarshal([]byte(value), &items); err != nil {
				return nil, err
			}
			out[key] = items
			continue
		}
		if number, err := strconv.ParseInt(value, 10, 64); err == nil {
			out[key] = number
			continue
		}
		out[key] = value
	}
	return out, nil
}

func parseAnnotationKeys(keys []string) (map[string]any, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("at least one field is required")
	}
	out := map[string]any{}
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" {
			return nil, fmt.Errorf("annotation key is required")
		}
		out[key] = nil
	}
	return out, nil
}

func mustFlag(cmd *cobra.Command, name string) string {
	return cmd.Flag(name).Value.String()
}

func mustBoolFlag(cmd *cobra.Command, name string) bool {
	value, _ := strconv.ParseBool(cmd.Flag(name).Value.String())
	return value
}

func mustIntFlag(cmd *cobra.Command, name string) int {
	value, _ := strconv.Atoi(cmd.Flag(name).Value.String())
	return value
}

func mustIntFlag64(cmd *cobra.Command, name string) int64 {
	value, _ := strconv.ParseInt(cmd.Flag(name).Value.String(), 10, 64)
	return value
}
