# Scenario 5: Build and Deploy

## Description
Once implementation is complete, components are built and deployed through OpenChoreo's pipeline (dev → stage → prod). Same UX as Integration Platform.

## User Scenarios

### 1. Trigger Build
- User triggers a build for a component (or auto-triggered on implementation completion)
- System creates an OpenChoreo WorkflowRun
- User sees build progress and logs

### 2. View Build History
- User views build history for a component
- Each build shows: status, commit, image, timestamp

### 3. Deploy to Environment
- User deploys a built component to an environment (dev/stage/prod)
- System creates an OpenChoreo ReleaseBinding
- User sees deployment status

### 4. Promote Across Environments
- User promotes a deployment from dev → stage → prod
- System follows the OpenChoreo DeploymentPipeline rules

### 5. View Deployment Status
- User views which version of each component is deployed in each environment
