# OpenChoreo Client Layer — Component Design

## Overview

Go client library wrapping OpenChoreo's control plane API. Reuses patterns from Agent Manager's `openchoreosvc/client` package.

## Capabilities

| Operation | OC Resource | Used By |
|-----------|-------------|---------|
| Create/list/delete projects | Project | ASDLC API Service |
| Create/update/delete components | Component | Implementation orchestration |
| Trigger builds | WorkflowRun | Build operations |
| Get build status/logs | WorkflowRun | Build monitoring |
| Deploy (create release bindings) | ReleaseBinding | Deploy operations |
| List environments | Environment | Environment management |
| Query logs/metrics/traces | Observer API | Manage/observe views |

## Reference

- Agent Manager client: `~/repos/agent-manager` → `openchoreosvc/client` package
- Integration Platform client: `~/repos/integration-platform` → `clients/openchoreo/` package
- OpenChoreo API docs: `/Users/wso2/openchoreo-sources/openchoreo.github.io/docs/reference/api/`
