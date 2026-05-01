import * as React from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';

import { cn } from '@/lib/utils';

const badgeVariants = cva(
  [
    'inline-flex w-fit shrink-0 items-center justify-center gap-1 overflow-hidden whitespace-nowrap',
    'rounded-full border px-2.5 py-1 text-[11px] font-semibold tracking-tight',
    'transition-[color,background-color,border-color,box-shadow]',
    "[&>svg]:size-3.5 [&>svg]:pointer-events-none",
    'focus-visible:ring-[3px] focus-visible:ring-[rgb(var(--primary-rgb)/0.32)]',
    'aria-invalid:ring-destructive/20 aria-invalid:border-destructive',
  ].join(' '),
  {
    variants: {
      variant: {
        default: 'border-transparent bg-[linear-gradient(135deg,#4F46E5_0%,#7C3AED_100%)] text-white shadow-[0_4px_12px_-4px_rgba(79,70,229,0.45)]',
        secondary: 'border-[color-mix(in_oklab,var(--primary)_22%,transparent)] bg-[color-mix(in_oklab,var(--primary)_10%,var(--card))] text-[color-mix(in_oklab,var(--primary)_70%,var(--foreground))]',
        destructive: 'border-transparent bg-destructive text-white focus-visible:ring-destructive/20',
        outline: 'border-border bg-card text-foreground hover:bg-muted',
        soft: 'border-[color-mix(in_oklab,var(--secondary)_24%,transparent)] bg-[color-mix(in_oklab,var(--secondary)_10%,var(--card))] text-[color-mix(in_oklab,var(--secondary)_70%,var(--foreground))]',
        success: 'border-[color-mix(in_oklab,#10B981_28%,transparent)] bg-[color-mix(in_oklab,#10B981_14%,var(--card))] text-[color-mix(in_oklab,#10B981_55%,var(--foreground))]',
      },
    },
    defaultVariants: {
      variant: 'default',
    },
  },
);

function Badge({
  className,
  variant,
  asChild = false,
  ...props
}: React.ComponentProps<'span'> &
  VariantProps<typeof badgeVariants> & {
    asChild?: boolean;
  }) {
  const Comp = asChild ? Slot : 'span';

  return (
    <Comp
      data-slot="badge"
      className={cn(badgeVariants({ variant }), className)}
      {...props}
    />
  );
}

export { Badge, badgeVariants };
