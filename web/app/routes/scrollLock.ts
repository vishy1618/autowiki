export function pinnedOnWheel(deltaY: number, currentlyPinned: boolean): boolean {
  if (deltaY < 0) return false;
  return currentlyPinned;
}

export function pinnedOnScroll(nearBottom: boolean, currentlyPinned: boolean): boolean {
  if (nearBottom) return true;
  return currentlyPinned;
}

export function isNearBottom(
  scrollTop: number,
  scrollHeight: number,
  clientHeight: number,
  threshold = 100,
): boolean {
  return scrollHeight - scrollTop - clientHeight <= threshold;
}
