import type { ClearanceRule, ViewerRole } from './api';

// isRestrictedForViewer reports whether a bead is hidden from the current
// viewer given its clearance rules. Lives in its own module so it can be
// unit-tested and reused without pulling in component code.
export const isRestrictedForViewer = (
  rules: ClearanceRule[] | undefined,
  viewerRoles: ViewerRole[],
): boolean => {
  if (!rules || rules.length === 0) return false;

  // Emergency and system roles always have access.
  if (viewerRoles.includes('emergency') || viewerRoles.includes('system')) {
    return false;
  }

  const now = new Date();

  for (const rule of rules) {
    // Skip expired rules.
    if (rule.expires_at) {
      const expiresAt = new Date(rule.expires_at);
      if (now > expiresAt) continue;
    }

    // Blacklist: restricted if any of the viewer's roles is denied.
    for (const viewerRole of viewerRoles) {
      if (rule.denied_roles.includes(viewerRole)) {
        return true;
      }
    }

    // Whitelist: when allowed_roles is set, the viewer must hold at least one
    // of those roles, otherwise the bead is restricted.
    if (rule.allowed_roles && rule.allowed_roles.length > 0) {
      const inAllowList = viewerRoles.some((role) => rule.allowed_roles!.includes(role));
      if (!inAllowList) {
        return true;
      }
    }
  }

  return false;
};
