import { useEffect } from "react";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * Wires up the minimum keyboard/focus behavior a floating panel needs to
 * behave like a real dialog instead of a div that merely looks like one
 * (see docs/audit-ux-2026-07.md §1.2): Escape closes it, focus moves into
 * it on open and is trapped inside while it's open (Tab/Shift+Tab wrap
 * around instead of escaping to the map/page behind it), and focus returns
 * to whatever triggered it on close.
 *
 * containerRef must point at the dialog's root element; only active while
 * open is true. initialFocusRef optionally overrides which element gets
 * focus on open (e.g. a search input rather than the DOM-first focusable
 * element, which might just be a close button).
 */
export function useDialogA11y(containerRef, open, onClose, initialFocusRef) {
  useEffect(() => {
    if (!open) return undefined;

    const previouslyFocused = document.activeElement;
    const container = containerRef.current;
    const focusable = container ? Array.from(container.querySelectorAll(FOCUSABLE_SELECTOR)) : [];
    (initialFocusRef?.current ?? focusable[0] ?? container)?.focus();

    function handleKeyDown(e) {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== "Tab" || !container) return;
      const items = Array.from(container.querySelectorAll(FOCUSABLE_SELECTOR));
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      if (previouslyFocused instanceof HTMLElement) previouslyFocused.focus();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, onClose, containerRef]);
}
