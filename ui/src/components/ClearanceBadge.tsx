import { ShieldAlert } from 'lucide-react';

interface ClearanceBadgeProps {
  compact?: boolean;
}

// Renders purely from a boolean "is this bead restricted for the current
// viewer" signal (see TimelineItem.restricted / R8b). Deliberately does NOT
// show denied-roles/reason/expiry: that rule detail is itself sensitive
// information, and surfacing it to the very viewer being denied access is a
// security regression, not a convenience. Callers that need to manage rule
// detail (e.g. an admin editing access) use ClearanceEditor + fetchClearanceRules
// directly, on demand, for a single bead — never this badge.
export function ClearanceBadge({ compact = false }: ClearanceBadgeProps) {
  if (compact) {
    return (
      <div
        className="flex items-center gap-1 px-1.5 py-0.5 bg-red-100 text-red-700 rounded text-xs font-medium"
        title="Restricted"
      >
        <ShieldAlert className="w-3 h-3" />
      </div>
    );
  }

  return (
    <div className="flex items-center gap-2 px-2 py-1 bg-red-50 border border-red-200 rounded-lg">
      <ShieldAlert className="w-4 h-4 text-red-600" />
      <span className="text-xs text-red-700 font-medium">Restricted</span>
    </div>
  );
}
