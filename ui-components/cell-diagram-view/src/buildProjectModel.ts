import type { Component, ComponentType, Connection, Project } from '@wso2/cell-diagram';

/**
 * One external HTTP API a component depends on at runtime. The diagram
 * renders each entry as a chain-link node *outside* the cell on the east
 * side, connected to the consuming component. Mirrors the architect's
 * `DependentApi` zod schema and the BFF's `models.DependentAPI`.
 */
export interface CellDiagramDependentApi {
  name: string;
  url: string;
  description?: string;
  authentication?: string;
}

/**
 * Structural shape consumed by {@link buildProjectModel}. Any object with these
 * fields (e.g. the console's `DesignComponent` API type) is acceptable.
 */
export interface CellDiagramComponent {
  name: string;
  componentType: string;
  language?: string;
  dependsOn?: string[];
  dependentApis?: CellDiagramDependentApi[];
}

const TYPE_MAP: Record<string, ComponentType> = {
  'web-app': 'web-app' as ComponentType,
  service: 'service' as ComponentType,
};

const PROJECT_ID = 'project';
// Synthetic "project" segment used in the dependent-API connection id. The
// cell-diagram lib treats any Connection whose id's project segment differs
// from the current project's id as external and lays it out on the east
// bound. Keeping a fixed value here groups all external APIs under one
// virtual umbrella in the diagram.
const EXTERNAL_API_PROJECT_SEGMENT = 'external-apis';

function dependentApiConnection(api: CellDiagramDependentApi): Connection {
  const tooltipParts: string[] = [api.url];
  if (api.description) tooltipParts.push(api.description);
  if (api.authentication) tooltipParts.push(`auth: ${api.authentication}`);
  return {
    id: `default:${EXTERNAL_API_PROJECT_SEGMENT}:${api.name}`,
    label: api.name,
    tooltip: tooltipParts.join(' — '),
  };
}

export function buildProjectModel(components: CellDiagramComponent[]): Project {
  const mapped: Component[] = components.map((comp) => {
    const siblingConnections: Connection[] = (comp.dependsOn || []).map(
      (depName) => ({
        id: `default:${PROJECT_ID}:${depName}`,
        label: depName,
        onPlatform: true,
      }),
    );
    const externalConnections: Connection[] = (comp.dependentApis || []).map(
      dependentApiConnection,
    );

    return {
      id: comp.name,
      label: comp.name,
      version: '1.0.0',
      type: TYPE_MAP[comp.componentType] ?? ('service' as ComponentType),
      buildPack: comp.language,
      services:
        comp.componentType === 'web-app'
          ? {
              [`${comp.name}:web`]: {
                id: `${comp.name}:web`,
                label: 'WebApp',
                type: 'HTTP',
                dependencyIds: (comp.dependsOn || []).map((dep) => `${dep}:api`),
                deploymentMetadata: {
                  gateways: { internet: { isExposed: true }, intranet: { isExposed: false } },
                },
              },
            }
          : comp.componentType === 'service'
            ? {
                [`${comp.name}:api`]: {
                  id: `${comp.name}:api`,
                  label: 'API',
                  type: 'HTTP',
                  dependencyIds: (comp.dependsOn || []).map((dep) => `${dep}:api`),
                  deploymentMetadata: {
                    gateways: { internet: { isExposed: false }, intranet: { isExposed: false } },
                  },
                },
              }
            : {},
      connections: [...siblingConnections, ...externalConnections],
    };
  });

  return {
    id: PROJECT_ID,
    name: 'Architecture',
    modelVersion: '0.2.0',
    components: mapped,
  };
}
