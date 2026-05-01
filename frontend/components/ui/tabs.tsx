'use client';

import * as React from 'react';
import * as TabsPrimitive from '@radix-ui/react-tabs';

import {cn} from '@/lib/utils';

function Tabs({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Root>) {
  return (
    <TabsPrimitive.Root
      data-slot="tabs"
      className={cn('flex flex-col gap-2', className)}
      {...props}
    />
  );
}

function TabsList({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      className={cn(
          'inline-flex h-9 w-fit items-center justify-center gap-0.5 rounded-xl border border-border/60 bg-[color-mix(in_oklab,var(--muted)_70%,var(--card))] p-1 text-muted-foreground',
          className,
      )}
      {...props}
    />
  );
}

function TabsTrigger({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
          'inline-flex h-[calc(100%-2px)] flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg border border-transparent px-3 py-1 text-sm font-semibold text-muted-foreground transition-[color,background-color,box-shadow,transform] outline-none data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-[var(--shadow-soft)] data-[state=active]:border-[color-mix(in_oklab,var(--primary)_18%,var(--border))] hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-[rgb(var(--primary-rgb)/0.32)] disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*=\'size-\'])]:size-4',
          className,
      )}
      {...props}
    />
  );
}

function TabsContent({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      data-slot="tabs-content"
      className={cn('flex-1 outline-none', className)}
      {...props}
    />
  );
}

export {Tabs, TabsList, TabsTrigger, TabsContent};
