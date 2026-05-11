package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const githubGraphQLEndpoint = "https://api.github.com/graphql"

// GitHubV2Client abstracts GitHub GraphQL API calls for Projects v2.
type GitHubV2Client interface {
	// GetOrgID returns the node ID for a GitHub organization (required for project mutations).
	GetOrgID(ctx context.Context, org, pat string) (nodeID string, err error)
	CreateGitHubV2Project(ctx context.Context, orgID, pat, title string) (projectID string, err error)
	LinkProjectToRepository(ctx context.Context, githubProjectID, owner, repo, pat string) error
	GetProjectBoard(ctx context.Context, githubProjectID, pat string) (*ProjectBoardResult, error)
	AddIssueToProject(ctx context.Context, githubProjectID, issueNodeID, pat string) error
	MoveProjectItemToStatus(ctx context.Context, githubProjectID, issueURL, targetStatus, pat string) error
}

// LabelInfo holds a GitHub label's name and hex color (without the leading #).
type LabelInfo struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// BoardItem is a single item on a GitHub Project v2 board.
type BoardItem struct {
	ID       string
	Title    string
	URL      string
	Body     string
	Assignee string
	Labels   []LabelInfo
	Status   string
}

// ProjectBoardResult holds all items fetched from a GitHub Project v2.
type ProjectBoardResult struct {
	URL   string
	Items []BoardItem
}

type githubV2Client struct {
	httpClient *http.Client
}

func NewGitHubV2Client() GitHubV2Client {
	return &githubV2Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// graphqlRequest sends a GraphQL query/mutation and decodes the response body into dest.
func (c *githubV2Client) graphqlRequest(ctx context.Context, pat, query string, variables map[string]any, dest any) error {
	payload := map[string]any{"query": query}
	if len(variables) > 0 {
		payload["variables"] = variables
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubGraphQLEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github graphql request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github graphql request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Check for GraphQL-level errors.
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("github graphql error: %s", envelope.Errors[0].Message)
	}

	if dest != nil {
		if err := json.Unmarshal(envelope.Data, dest); err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
	}
	return nil
}

func (c *githubV2Client) GetOrgID(ctx context.Context, org, pat string) (string, error) {
	slog.InfoContext(ctx, "resolving org node ID", "org", org)
	query := `
		query($login: String!) {
			organization(login: $login) {
				id
			}
		}`

	var data struct {
		Organization struct {
			ID string `json:"id"`
		} `json:"organization"`
	}
	if err := c.graphqlRequest(ctx, pat, query, map[string]any{"login": org}, &data); err != nil {
		return "", err
	}
	if data.Organization.ID == "" {
		return "", fmt.Errorf("organization %q not found", org)
	}
	slog.InfoContext(ctx, "org node ID resolved", "org", org, "nodeId", data.Organization.ID)
	return data.Organization.ID, nil
}

func (c *githubV2Client) CreateGitHubV2Project(ctx context.Context, orgID, pat, title string) (string, error) {
	slog.InfoContext(ctx, "creating GitHub project", "title", title)
	mutation := `
		mutation($ownerId: ID!, $title: String!) {
			createProjectV2(input: { ownerId: $ownerId, title: $title }) {
				projectV2 {
					id
					url
				}
			}
		}`

	var data struct {
		CreateProjectV2 struct {
			ProjectV2 struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"projectV2"`
		} `json:"createProjectV2"`
	}
	if err := c.graphqlRequest(ctx, pat, mutation, map[string]any{"ownerId": orgID, "title": title}, &data); err != nil {
		return "", err
	}

	p := data.CreateProjectV2.ProjectV2
	if p.ID == "" {
		return "", fmt.Errorf("createProjectV2 returned empty project ID")
	}

	// Projects are private by default — make it public so the URL is accessible.
	updateMutation := `
		mutation($projectId: ID!) {
			updateProjectV2(input: { projectId: $projectId, public: true }) {
				projectV2 {
					url
				}
			}
		}`
	var updateData struct {
		UpdateProjectV2 struct {
			ProjectV2 struct {
				URL string `json:"url"`
			} `json:"projectV2"`
		} `json:"updateProjectV2"`
	}
	if err := c.graphqlRequest(ctx, pat, updateMutation, map[string]any{"projectId": p.ID}, &updateData); err != nil {
		slog.WarnContext(ctx, "failed to make project public", "projectId", p.ID, "error", err)
	}

	url := updateData.UpdateProjectV2.ProjectV2.URL
	slog.InfoContext(ctx, "GitHub project created", "title", title, "projectId", p.ID, "url", url)

	// Initialize the Status field with all 5 options (Todo, In Progress, Done, On Hold, Failed).
	if err := c.initProjectStatusOptions(ctx, p.ID, pat); err != nil {
		slog.WarnContext(ctx, "failed to init project status options", "projectId", p.ID, "error", err)
		// non-fatal — board still works with the 3 default columns
	}

	return p.ID, nil
}

// initProjectStatusOptions initializes the Status field with all 5 options: Todo, In Progress, Done, On Hold, Failed.
// It queries the existing Status field ID and options, then adds any missing options (On Hold, Failed).
func (c *githubV2Client) initProjectStatusOptions(ctx context.Context, projectID, pat string) error {
	// Query the Status field and its existing options.
	query := `
		query($projectId: ID!) {
			node(id: $projectId) {
				... on ProjectV2 {
					fields(first: 20) {
						nodes {
							... on ProjectV2SingleSelectField {
								id
								name
								options { id name }
							}
						}
					}
				}
			}
		}`

	var data struct {
		Node struct {
			Fields struct {
				Nodes []struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Options []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"options"`
				} `json:"nodes"`
			} `json:"fields"`
		} `json:"node"`
	}
	if err := c.graphqlRequest(ctx, pat, query, map[string]any{"projectId": projectID}, &data); err != nil {
		return fmt.Errorf("query project status field: %w", err)
	}

	// Find the Status field.
	var statusFieldID string
	existingSet := make(map[string]string) // name -> id
	for _, f := range data.Node.Fields.Nodes {
		if f.Name == "Status" {
			statusFieldID = f.ID
			for _, opt := range f.Options {
				existingSet[opt.Name] = opt.ID
			}
			break
		}
	}
	if statusFieldID == "" {
		return fmt.Errorf("status field not found in project")
	}

	// Build the full option list: preserve existing options (with their IDs) and add missing ones.
	// Colors: Todo (GRAY), In Progress (YELLOW), Done (GREEN), On Hold (ORANGE), Failed (RED).
	optionNames := []string{"Todo", "In Progress", "Done", "On Hold", "Failed"}
	optionColors := map[string]string{
		"Todo":         "GRAY",
		"In Progress":  "YELLOW",
		"Done":         "GREEN",
		"On Hold":      "ORANGE",
		"Failed":       "RED",
	}

	// Build the input options list: preserve existing (with ID), add missing (without ID).
	type optionInput struct {
		ID    string `json:"id,omitempty"`
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	var inputOptions []optionInput
	for _, name := range optionNames {
		if existingID, exists := existingSet[name]; exists {
			// Preserve existing option with its ID.
			inputOptions = append(inputOptions, optionInput{
				ID:    existingID,
				Name:  name,
				Color: optionColors[name],
			})
		} else {
			// Add new option without ID (GitHub will create it).
			inputOptions = append(inputOptions, optionInput{
				Name:  name,
				Color: optionColors[name],
			})
		}
	}

	// Mutation to update the Status field with all options.
	mutation := `
		mutation($projectId: ID!, $fieldId: ID!, $options: [ProjectV2SingleSelectFieldOptionInput!]!) {
			updateProjectV2Field(input: {
				projectId: $projectId
				fieldId: $fieldId
				singleSelectOptions: $options
			}) {
				projectV2Field {
					... on ProjectV2SingleSelectField { id }
				}
			}
		}`

	if err := c.graphqlRequest(ctx, pat, mutation, map[string]any{
		"projectId": projectID,
		"fieldId":   statusFieldID,
		"options":   inputOptions,
	}, nil); err != nil {
		return fmt.Errorf("update project status field options: %w", err)
	}

	slog.InfoContext(ctx, "initialized project status field options", "projectId", projectID)
	return nil
}

func (c *githubV2Client) GetProjectBoard(ctx context.Context, githubProjectID, pat string) (*ProjectBoardResult, error) {
	// Query the project board directly by its node ID.
	query := `
		query($projectId: ID!) {
			node(id: $projectId) {
				... on ProjectV2 {
					url
					items(first: 100) {
						nodes {
							id
							fieldValues(first: 10) {
								nodes {
									... on ProjectV2ItemFieldSingleSelectValue {
										name
										field {
											... on ProjectV2SingleSelectField { name }
										}
									}
								}
							}
							content {
								... on Issue {
									title
									url
									body
									assignees(first: 1) {
										nodes { login }
									}
									labels(first: 10) {
										nodes { name color }
									}
								}
							}
						}
					}
				}
			}
		}`

	var data struct {
		Node struct {
			URL   string `json:"url"`
			Items struct {
				Nodes []struct {
					ID          string `json:"id"`
					FieldValues struct {
						Nodes []struct {
							Name  string `json:"name"`
							Field struct {
								Name string `json:"name"`
							} `json:"field"`
						} `json:"nodes"`
					} `json:"fieldValues"`
					Content struct {
						Title     string `json:"title"`
						URL       string `json:"url"`
						Body      string `json:"body"`
						Assignees struct {
							Nodes []struct {
								Login string `json:"login"`
							} `json:"nodes"`
						} `json:"assignees"`
						Labels struct {
							Nodes []struct {
								Name  string `json:"name"`
								Color string `json:"color"`
							} `json:"nodes"`
						} `json:"labels"`
					} `json:"content"`
				} `json:"nodes"`
			} `json:"items"`
		} `json:"node"`
	}

	if err := c.graphqlRequest(ctx, pat, query, map[string]any{"projectId": githubProjectID}, &data); err != nil {
		return nil, fmt.Errorf("get project board: %w", err)
	}

	result := &ProjectBoardResult{URL: data.Node.URL, Items: []BoardItem{}}

	for _, node := range data.Node.Items.Nodes {
		if node.Content.Title == "" {
			continue // skip draft / non-issue items
		}

		var status string
		for _, fv := range node.FieldValues.Nodes {
			if fv.Field.Name == "Status" {
				status = fv.Name
				break
			}
		}

		labels := make([]LabelInfo, 0, len(node.Content.Labels.Nodes))
		for _, l := range node.Content.Labels.Nodes {
			labels = append(labels, LabelInfo{Name: l.Name, Color: l.Color})
		}

		var assignee string
		if len(node.Content.Assignees.Nodes) > 0 {
			assignee = node.Content.Assignees.Nodes[0].Login
		}

		result.Items = append(result.Items, BoardItem{
			ID:       node.ID,
			Title:    node.Content.Title,
			URL:      node.Content.URL,
			Body:     node.Content.Body,
			Assignee: assignee,
			Labels:   labels,
			Status:   status,
		})
	}

	slog.InfoContext(ctx, "fetched project board", "projectId", githubProjectID, "items", len(result.Items))
	return result, nil
}

func (c *githubV2Client) AddIssueToProject(ctx context.Context, githubProjectID, issueNodeID, pat string) error {
	addMutation := `
		mutation($projectId: ID!, $contentId: ID!) {
			addProjectV2ItemById(input: { projectId: $projectId, contentId: $contentId }) {
				item { id }
			}
		}`

	var addData struct {
		AddProjectV2ItemById struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	if err := c.graphqlRequest(ctx, pat, addMutation, map[string]any{
		"projectId": githubProjectID,
		"contentId": issueNodeID,
	}, &addData); err != nil {
		return fmt.Errorf("add item to project: %w", err)
	}

	slog.InfoContext(ctx, "added issue to project board",
		"projectId", githubProjectID, "itemId", addData.AddProjectV2ItemById.Item.ID)
	return nil
}

func (c *githubV2Client) MoveProjectItemToStatus(ctx context.Context, githubProjectID, issueURL, targetStatus, pat string) error {
	query := `
		query($projectId: ID!) {
			node(id: $projectId) {
				... on ProjectV2 {
					fields(first: 20) {
						nodes {
							... on ProjectV2SingleSelectField {
								id
								name
								options { id name }
							}
						}
					}
					items(first: 100) {
						nodes {
							id
							content {
								... on Issue { url }
							}
						}
					}
				}
			}
		}`

	var data struct {
		Node struct {
			Fields struct {
				Nodes []struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Options []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"options"`
				} `json:"nodes"`
			} `json:"fields"`
			Items struct {
				Nodes []struct {
					ID      string `json:"id"`
					Content struct {
						URL string `json:"url"`
					} `json:"content"`
				} `json:"nodes"`
			} `json:"items"`
		} `json:"node"`
	}
	if err := c.graphqlRequest(ctx, pat, query, map[string]any{"projectId": githubProjectID}, &data); err != nil {
		return fmt.Errorf("query project fields/items: %w", err)
	}

	var fieldID, optionID string
	for _, f := range data.Node.Fields.Nodes {
		if f.Name == "Status" {
			fieldID = f.ID
			for _, opt := range f.Options {
				if opt.Name == targetStatus {
					optionID = opt.ID
					break
				}
			}
			break
		}
	}
	if fieldID == "" || optionID == "" {
		slog.WarnContext(ctx, "status field or option not found, skipping board move",
			"projectId", githubProjectID, "targetStatus", targetStatus)
		return nil
	}

	var itemID string
	for _, item := range data.Node.Items.Nodes {
		if item.Content.URL == issueURL {
			itemID = item.ID
			break
		}
	}
	if itemID == "" {
		slog.WarnContext(ctx, "board item not found for issue, skipping board move",
			"projectId", githubProjectID, "issueURL", issueURL)
		return nil
	}

	mutation := `
		mutation($projectId: ID!, $itemId: ID!, $fieldId: ID!, $optionId: String!) {
			updateProjectV2ItemFieldValue(input: {
				projectId: $projectId
				itemId: $itemId
				fieldId: $fieldId
				value: { singleSelectOptionId: $optionId }
			}) {
				projectV2Item { id }
			}
		}`

	if err := c.graphqlRequest(ctx, pat, mutation, map[string]any{
		"projectId": githubProjectID,
		"itemId":    itemID,
		"fieldId":   fieldID,
		"optionId":  optionID,
	}, nil); err != nil {
		return fmt.Errorf("update project item status: %w", err)
	}

	slog.InfoContext(ctx, "moved project board item",
		"projectId", githubProjectID, "itemId", itemID, "status", targetStatus)
	return nil
}

func (c *githubV2Client) LinkProjectToRepository(ctx context.Context, githubProjectID, owner, repo, pat string) error {
	// Resolve the repository node ID.
	repoQuery := `
		query($owner: String!, $repo: String!) {
			repository(owner: $owner, name: $repo) {
				id
			}
		}`

	var repoData struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if err := c.graphqlRequest(ctx, pat, repoQuery, map[string]any{"owner": owner, "repo": repo}, &repoData); err != nil {
		return fmt.Errorf("resolve repository id: %w", err)
	}
	if repoData.Repository.ID == "" {
		return fmt.Errorf("repository %s/%s not found", owner, repo)
	}

	linkMutation := `
		mutation($projectId: ID!, $repositoryId: ID!) {
			linkProjectV2ToRepository(input: { projectId: $projectId, repositoryId: $repositoryId }) {
				repository { id }
			}
		}`

	if err := c.graphqlRequest(ctx, pat, linkMutation, map[string]any{
		"projectId":    githubProjectID,
		"repositoryId": repoData.Repository.ID,
	}, nil); err != nil {
		return fmt.Errorf("link project to repository: %w", err)
	}

	slog.InfoContext(ctx, "linked project to repository", "projectId", githubProjectID, "owner", owner, "repo", repo)
	return nil
}
