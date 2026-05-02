import * as React from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';

import { cn } from '@/lib/utils';

const buttonVariants = cva(
  [
    'inline-flex cursor-pointer items-center justify-center gap-2 whitespace-nowrap text-sm font-semibold tracking-tight',
    'transition-[transform,background-color,color,border-color,box-shadow] duration-200',
    'disabled:pointer-events-none disabled:opacity-50 shrink-0',
    "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
    'outline-none focus-visible:ring-[3px] focus-visible:ring-[rgb(var(--primary-rgb)/0.32)] focus-visible:ring-offset-2 focus-visible:ring-offset-background',
    'aria-invalid:ring-destructive/20 aria-invalid:border-destructive',
  ].join(' '),
  {
    variants: {
      variant: {
        default: [
          'border border-transparent text-white',
          'bg-[linear-gradient(135deg,#4F46E5_0%,#7C3AED_100%)]',
          'shadow-[var(--shadow-button)]',
          'hover:-translate-y-px hover:shadow-[var(--shadow-button-hover)]',
          'hover:brightness-[1.04]',
        ].join(' '),
        destructive: 'border border-transparent bg-destructive text-white shadow-sm hover:-translate-y-px hover:bg-destructive/92',
        outline: 'border border-border bg-card/80 text-foreground shadow-[0_2px_8px_-4px_rgba(15,23,42,0.10)] hover:-translate-y-px hover:border-[color-mix(in_oklab,var(--primary)_38%,var(--border))] hover:bg-card hover:shadow-[var(--shadow-soft)]',
        secondary: 'border border-transparent bg-secondary text-secondary-foreground hover:bg-[color-mix(in_oklab,var(--secondary)_78%,transparent)]',
        ghost: 'border border-transparent text-muted-foreground hover:bg-muted hover:text-foreground',
        link: 'text-primary underline-offset-4 hover:underline',
        soft: 'border border-[color-mix(in_oklab,var(--primary)_22%,transparent)] bg-[color-mix(in_oklab,var(--primary)_10%,var(--card))] text-[color-mix(in_oklab,var(--primary)_70%,var(--foreground))] hover:-translate-y-px hover:bg-[color-mix(in_oklab,var(--primary)_15%,var(--card))]',
      },
      size: {
        default: 'h-10 px-4 py-2 has-[>svg]:px-3.5',
        sm: 'h-9 gap-1.5 px-3 has-[>svg]:px-2.5',
        lg: 'h-11 px-6 has-[>svg]:px-4 text-[15px]',
        icon: 'size-10',
        pill: 'h-10 rounded-full px-5',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
);

function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: React.ComponentProps<'button'> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  }) {
  const Comp = asChild ? Slot : 'button';

  return (
    <Comp
      data-slot="button"
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  );
}

export { Button, buttonVariants };
