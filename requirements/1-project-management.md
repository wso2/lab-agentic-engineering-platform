# Scenario 1: Project Management

## Description
User creates and manages projects within an organization. Each project maps to a Git monorepo and an OpenChoreo Project.

## User Scenarios

### 1. Create Project
- User navigates to organization view
- User clicks "New Project"
- User enters project name, description
- User provides a Git repo URL (empty repo with bot installed)
- System creates OpenChoreo Project and scaffolds the monorepo
- User sees the new project in their project list

### 2. View Project List
- User navigates to organization view
- User sees all projects with their current lifecycle phase (spec/design/implementation/deployed)

### 3. View Project Details
- User clicks on a project
- User sees tabs: Spec, Design, Components, Deploy, Manage
- Current lifecycle phase is highlighted

### 4. Delete Project
- User deletes a project
- System cleans up OpenChoreo Project and associated resources
- Git repo is NOT deleted (user's responsibility)
