import { Button, Chip, Menu, MenuItem, Stack, Tooltip, Typography } from '@wso2/oxygen-ui';
import { ChevronRight } from '@wso2/oxygen-ui-icons-react';
import { useState } from 'react';
import type { ArtifactVersion } from '../services/api/types';

interface VersionSelectorProps {
  versions: ArtifactVersion[];
  currentVersion: number;
  onVersionSelect: (version: number) => void;
  /** When true, the user is viewing a historical (non-latest) version */
  isHistorical?: boolean;
  /** When true, working copy differs from the latest tagged version */
  hasUnsavedChanges?: boolean;
  /** Called when the user clicks "Discard" to revert to the latest tagged version */
  onDiscard?: () => void;
  /** When true, a discard operation is in progress */
  isDiscarding?: boolean;
}

export default function VersionSelector({
  versions,
  currentVersion,
  onVersionSelect,
  isHistorical,
  hasUnsavedChanges,
  onDiscard,
  isDiscarding,
}: VersionSelectorProps) {
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);

  if (versions.length === 0) return null;

  const latestVersion = Math.max(...versions.map((v) => v.version));

  // Disable version switching when there are unsaved changes (switching would lose them)
  const switchDisabled = hasUnsavedChanges && !isHistorical;

  const chipElement = (
    <Chip
      label={
        <Stack direction="row" alignItems="center" gap={0.5}>
          <Typography variant="caption" fontWeight={600} sx={{ fontSize: '0.75rem' }}>
            {currentVersion > 0 ? `v${currentVersion}` : 'Draft'}
            {currentVersion === latestVersion && !isHistorical ? ' (latest)' : ''}
          </Typography>
          {!switchDisabled && (
            <ChevronRight
              size={12}
              style={{
                transform: anchorEl ? 'rotate(90deg)' : 'rotate(0deg)',
                transition: 'transform 0.2s',
              }}
            />
          )}
        </Stack>
      }
      size="small"
      variant={isHistorical ? 'filled' : 'outlined'}
      color={isHistorical ? 'warning' : 'default'}
      onClick={switchDisabled ? undefined : (e) => setAnchorEl(e.currentTarget)}
      sx={{ cursor: switchDisabled ? 'default' : 'pointer', opacity: switchDisabled ? 0.7 : 1 }}
    />
  );

  return (
    <>
      {switchDisabled ? (
        <Tooltip title="Discard changes or save before switching versions" placement="bottom">
          <span>{chipElement}</span>
        </Tooltip>
      ) : (
        chipElement
      )}
      <Menu
        anchorEl={anchorEl}
        open={Boolean(anchorEl)}
        onClose={() => setAnchorEl(null)}
      >
        {versions
          .slice()
          .sort((a, b) => b.version - a.version)
          .map((v) => (
            <MenuItem
              key={v.version}
              selected={v.version === currentVersion && !isHistorical}
              onClick={() => {
                onVersionSelect(v.version);
                setAnchorEl(null);
              }}
            >
              <Typography variant="body2">
                v{v.version}
                {v.version === latestVersion ? ' (latest)' : ''}
              </Typography>
            </MenuItem>
          ))}
      </Menu>
      {hasUnsavedChanges && !isHistorical && (
        <Stack direction="row" alignItems="center" gap={0.5}>
          <Chip
            label="Unsaved changes"
            size="small"
            color="warning"
            variant="outlined"
            sx={{ fontSize: '0.7rem', height: 24 }}
          />
          {onDiscard && (
            <Button
              variant="text"
              size="small"
              color="warning"
              onClick={onDiscard}
              disabled={isDiscarding}
              sx={{ textTransform: 'none', fontSize: '0.7rem', minWidth: 'auto', p: '2px 6px' }}
            >
              {isDiscarding ? 'Discarding...' : 'Discard'}
            </Button>
          )}
        </Stack>
      )}
    </>
  );
}
