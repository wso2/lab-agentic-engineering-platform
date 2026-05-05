import { Avatar, CircularProgress, Paper, Stack, Tooltip, Typography } from '@wso2/oxygen-ui';
import { Users } from '@wso2/oxygen-ui-icons-react';
import type { CollabPeer } from '../hooks/useCollabEditor';

interface Props {
  connected: boolean;
  peers: CollabPeer[];
  inToolbar?: boolean;
}

function peerInitials(name: string): string {
  return name.split(' ').map(w => w[0] ?? '').join('').toUpperCase().slice(0, 2);
}

export default function CollabAwarenessBar({ connected, peers, inToolbar = false }: Props) {
  const editingPeers = peers.filter(p => p.editing);
  const totalPeers = editingPeers.length + 1;

  return (
    <Paper
      variant="outlined"
      sx={{
        px: inToolbar ? 0 : 2,
        py: inToolbar ? 0 : 1,
        mb: inToolbar ? 0 : 2,
        borderRadius: inToolbar ? 0 : 2,
        border: inToolbar ? 'none' : undefined,
        bgcolor: inToolbar ? 'transparent' : undefined,
        display: 'flex',
        alignItems: 'center',
        gap: inToolbar ? 1 : 2,
      }}
    >
      <Stack direction="row" alignItems="center" gap={1}>
        <Users size={inToolbar ? 14 : 16} />
        {!connected ? (
          <>
            <CircularProgress size={14} />
            <Typography variant={inToolbar ? 'caption' : 'body2'} color="text.secondary">Connecting...</Typography>
          </>
        ) : (
          <>
            <Typography variant={inToolbar ? 'caption' : 'body2'} color="text.secondary">
              {totalPeers === 1 ? 'Only you' : `${totalPeers} people`} editing
            </Typography>
            <Stack direction="row" sx={{ ml: inToolbar ? 0.5 : 1 }}>
              {editingPeers.map(peer => (
                <Tooltip key={peer.clientId} title={peer.name}>
                  <Avatar
                    sx={{
                      width: inToolbar ? 22 : 28,
                      height: inToolbar ? 22 : 28,
                      fontSize: inToolbar ? '0.62rem' : '0.7rem',
                      bgcolor: peer.color, ml: -0.5, border: '2px solid white',
                    }}
                  >
                    {peerInitials(peer.name)}
                  </Avatar>
                </Tooltip>
              ))}
            </Stack>
          </>
        )}
      </Stack>
    </Paper>
  );
}
