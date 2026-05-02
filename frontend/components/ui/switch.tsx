'use client';

import * as React from 'react';
import * as SwitchPrimitive from '@radix-ui/react-switch';

import { cn } from '@/lib/utils';

function Switch({
  className,
  ...props
}: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        'peer inline-flex h-[22px] w-[40px] shrink-0 cursor-pointer items-center rounded-full border border-transparent transition-all outline-none',
        'data-[state=unchecked]:bg-[color-mix(in_oklab,var(--muted)_70%,var(--border))]',
        'data-[state=checked]:bg-[linear-gradient(135deg,#4F46E5_0%,#7C3AED_100%)]',
        'data-[state=checked]:shadow-[0_4px_12px_-4px_rgba(79,70,229,0.45)]',
        'focus-visible:ring-[3px] focus-visible:ring-[rgb(var(--primary-rgb)/0.32)]',
        'disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          'pointer-events-none block size-[18px] translate-x-0.5 rounded-full bg-white ring-0 transition-transform',
          'shadow-[0_2px_6px_-2px_rgba(15,23,42,0.45)]',
          'data-[state=checked]:translate-x-[20px]',
        )}
      />
    </SwitchPrimitive.Root>
  );
}

export { Switch };
