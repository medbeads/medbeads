import { ShieldAlert, Clock } from 'lucide-react';
import type { ClearanceRule, ViewerRole } from '../lib/api';
import { VIEWER_ROLES } from '../lib/api';

interface ClearanceBadgeProps {
  rules: ClearanceRule[];
  compact?: boolean;
}

export function ClearanceBadge({ rules, compact = false }: ClearanceBadgeProps) {
  if (!rules || rules.length === 0) {
    return null;
  }

  // Collect all denied roles
  const allDeniedRoles = new Set<ViewerRole>();
  let hasExpiring = false;

  rules.forEach(rule => {
    rule.denied_roles.forEach(role => allDeniedRoles.add(role));
    if (rule.expires_at) {
      hasExpiring = true;
    }
  });

  const getRoleLabel = (role: ViewerRole) => {
    const found = VIEWER_ROLES.find(r => r.value === role);
    return found ? found.labelJa : role;
  };

  if (compact) {
    return (
      <div
        className="flex items-center gap-1 px-1.5 py-0.5 bg-red-100 text-red-700 rounded text-xs font-medium"
        title={`Restricted: ${Array.from(allDeniedRoles).map(getRoleLabel).join(', ')}`}
      >
        <ShieldAlert className="w-3 h-3" />
        {hasExpiring && <Clock className="w-3 h-3" />}
      </div>
    );
  }

  return (
    <div className="flex items-center gap-2 px-2 py-1 bg-red-50 border border-red-200 rounded-lg">
      <ShieldAlert className="w-4 h-4 text-red-600" />
      <div className="text-xs text-red-700">
        <span className="font-medium">Restricted:</span>{' '}
        {Array.from(allDeniedRoles).map(getRoleLabel).join(', ')}
        {hasExpiring && (
          <span className="ml-2 inline-flex items-center gap-1 text-amber-600">
            <Clock className="w-3 h-3" />
            Temporary
          </span>
        )}
      </div>
    </div>
  );
}
