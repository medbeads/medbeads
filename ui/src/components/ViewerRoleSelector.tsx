import { VIEWER_ROLES, type ViewerRole } from '../lib/api';

interface ViewerRoleSelectorProps {
  selectedRoles: ViewerRole[];
  onRolesChange: (roles: ViewerRole[]) => void;
}

export function ViewerRoleSelector({ selectedRoles, onRolesChange }: ViewerRoleSelectorProps) {
  const selectSingleRole = (role: ViewerRole) => {
    onRolesChange([role]);
  };

  const currentRole = selectedRoles[0] || 'system';

  // Group roles for compact display
  const roleGroups = [
    { roles: ['patient', 'family'] as ViewerRole[], label: '患者側' },
    { roles: ['primary_care', 'specialist', 'nurse'] as ViewerRole[], label: '医療者' },
    { roles: ['admin', 'insurance'] as ViewerRole[], label: '事務' },
    { roles: ['emergency', 'system'] as ViewerRole[], label: '特権' },
  ];

  return (
    <div className="flex items-center gap-2 text-sm">
      <span className="text-slate-500 font-medium mr-1">閲覧者:</span>
      <div className="flex items-center gap-1 flex-wrap">
        {VIEWER_ROLES.map(role => {
          const isSelected = currentRole === role.value;
          const isSpecial = role.value === 'emergency' || role.value === 'system';

          return (
            <label
              key={role.value}
              className={`
                inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md cursor-pointer transition-all
                border text-xs font-medium
                ${isSelected
                  ? isSpecial
                    ? 'bg-amber-100 border-amber-400 text-amber-800'
                    : 'bg-blue-100 border-blue-400 text-blue-800'
                  : 'bg-white border-slate-200 text-slate-600 hover:bg-slate-50 hover:border-slate-300'
                }
              `}
            >
              <input
                type="radio"
                name="viewerRole"
                value={role.value}
                checked={isSelected}
                onChange={() => selectSingleRole(role.value)}
                className="sr-only"
              />
              <span
                className={`
                  w-2.5 h-2.5 rounded-full border-2 flex-shrink-0
                  ${isSelected
                    ? isSpecial
                      ? 'bg-amber-500 border-amber-500'
                      : 'bg-blue-500 border-blue-500'
                    : 'border-slate-300'
                  }
                `}
              />
              <span>{role.labelJa}</span>
            </label>
          );
        })}
      </div>
    </div>
  );
}
