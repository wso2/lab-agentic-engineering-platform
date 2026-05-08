import { useEffect, useRef, useState } from 'react';
import { Box } from '@wso2/oxygen-ui';

interface AnimatedBannerProps {
  show: boolean;
  children: React.ReactNode;
}

export function AnimatedBanner({ show, children }: AnimatedBannerProps) {
  const [rendered, setRendered] = useState(show);
  const [isVisible, setIsVisible] = useState(false);
  const exitTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => {
    if (show) {
      clearTimeout(exitTimer.current);
      setRendered(true);
      const raf = requestAnimationFrame(() =>
        requestAnimationFrame(() => setIsVisible(true))
      );
      return () => cancelAnimationFrame(raf);
    } else {
      setIsVisible(false);
      exitTimer.current = setTimeout(() => setRendered(false), 280);
      return () => clearTimeout(exitTimer.current);
    }
  }, [show]);

  if (!rendered) return null;

  return (
    <Box
      sx={{
        opacity: isVisible ? 1 : 0,
        transform: isVisible ? 'translateY(0)' : 'translateY(-6px)',
        transition: 'opacity 0.25s ease, transform 0.25s ease',
      }}
    >
      {children}
    </Box>
  );
}
