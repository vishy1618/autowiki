export function isNearBottom(
  scrollTop: number,
  scrollHeight: number,
  clientHeight: number,
  threshold = 100,
): boolean {
  return scrollHeight - scrollTop - clientHeight <= threshold;
}

interface PinnedStateArgs {
  userGesture: boolean;
  nearBottom: boolean;
  currentlyPinned: boolean;
}

export function nextPinnedState({ userGesture, nearBottom, currentlyPinned }: PinnedStateArgs): boolean {
  if (userGesture) return false;
  return nearBottom ? true : currentlyPinned;
}
