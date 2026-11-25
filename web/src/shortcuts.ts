import type {KeyboardEvent} from 'react';

export function workbenchShortcut(e: KeyboardEvent): string | undefined {
  if (e.defaultPrevented || e.nativeEvent.isComposing || e.repeat || e.altKey || e.shiftKey || (!e.ctrlKey && !e.metaKey)) return;
  if ((e.target as Element).closest('input:not([type=checkbox]), textarea, select, [contenteditable], [role=textbox]')) return;
  const key = e.key.toLowerCase();
  if (key === 'c' && window.getSelection()?.toString()) return;
  return key;
}
