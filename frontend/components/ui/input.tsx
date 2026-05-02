import * as React from 'react';

import { cn } from '@/lib/utils';

function Input({ className, type, ...props }: React.ComponentProps<'input'>) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        'placeholder:text-muted-foreground/80 selection:bg-primary/20 selection:text-foreground',
        'border-input file:text-foreground h-10 w-full min-w-0 border bg-card px-3.5 py-2 text-sm leading-6 outline-none',
        'transition-[color,box-shadow,border-color,background-color]',
        'file:inline-flex file:h-8 file:border-0 file:bg-transparent file:text-sm file:font-medium',
        'disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50',
        'aria-invalid:ring-destructive/20 aria-invalid:border-destructive',
        className,
      )}
      {...props}
    />
  );
}

export { Input };
