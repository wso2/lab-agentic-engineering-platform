# ASDLC Console — Component Design

## Overview

React SPA providing the developer-facing UI for the ASDLC platform. Styled with Oxygen UI to match the Integration Platform look-and-feel.

## Tech Stack

- React + TypeScript
- Vite (build/dev server)
- Oxygen UI (WSO2 design system, built on MUI)
- React Router (navigation)
- React Query (server state)
- Monaco Editor (spec editing)
- WebSocket (real-time agent status, build logs)

## Page Structure

```
/organizations/:orgId
  /projects                           → Project list
  /projects/new                       → Create project (name, repo URL)
  /projects/:projectId
    /spec                             → Spec editor / viewer
    /design                           → Design doc viewer
    /components                       → Component list with implementation status
    /components/:componentId
      /overview                       → Responsibilities, API boundaries
      /implementation                 → Agent progress, code diffs
      /build                          → Build history, logs
      /deploy                         → Deployment status per environment
      /logs                           → Runtime logs
    /deploy                           → Project-level deployment overview
    /manage                           → Environment management
```

## Key Views

- **Spec Editor**: Monaco with markdown preview, AI generation from prompt, version history
- **Design Viewer**: Architecture doc display, component graph, API boundary definitions
- **Component Board**: Cards per component showing implementation status (pending/in-progress/review/done)
- **Agent Activity**: Real-time agent output streaming via WebSocket
- **Build/Deploy**: Same patterns as Integration Platform (build history, deploy to environments)

## Package Structure (monorepo)

```
console/
├── apps/webapp/          → Main app shell, routing, layout
├── packages/
│   ├── api-client/       → Typed REST client for ASDLC API Service
│   ├── auth/             → JWT / OIDC auth
│   ├── shared-components/→ Reusable UI components
│   └── types/            → Shared TypeScript types
```
