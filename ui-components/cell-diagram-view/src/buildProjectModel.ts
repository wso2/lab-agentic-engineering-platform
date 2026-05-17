import type { ComponentType, Project } from '@wso2/cell-diagram';

/**
 * Structural shape consumed by {@link buildProjectModel}. Any object with these
 * fields (e.g. the console's `DesignComponent` API type) is acceptable.
 */
export interface CellDiagramComponent {
  name: string;
  componentType: string;
  language?: string;
  dependsOn?: string[];
}

const TYPE_MAP: Record<string, ComponentType> = {
  'web-app': 'web-app' as ComponentType,
  service: 'service' as ComponentType,
};

export function buildProjectModel(components: CellDiagramComponent[]): Project {
  return {
    id: 'project',
    name: 'Architecture',
    modelVersion: '0.2.0',
    components: components.map((comp) => ({
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
      connections: (comp.dependsOn || []).map((depName) => ({
        id: `default:project:${depName}`,
        label: depName,
        onPlatform: true,
      })),
    })),
  };
}
