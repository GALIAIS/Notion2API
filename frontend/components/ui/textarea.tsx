import * as React from 'react';

import { cn } from '@/lib/utils';

function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        'placeholder:text-muted-foreground/80 selection:bg-primary/20 selection:text-foreground',
        'border-input min-h-[88px] w-full min-w-0 border bg-card px-3.5 py-2.5 text-sm leading-6 outline-none',
        'transition-[color,box-shadow,border-color,background-color]',
        'disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50',
        'aria-invalid:ring-destructive/20 aria-invalid:border-destructive',
        className,
      )}
      {...props}
    />
  );
}

export { Textarea };
