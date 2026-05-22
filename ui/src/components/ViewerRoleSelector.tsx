import { VIEWER_ROLES, DEPARTMENTS, type ViewerRole } from '../lib/api';

interface ViewerRoleSelectorProps {
  selectedRoles: ViewerRole[];
  onRolesChange: (roles: ViewerRole[]) => void;
}

const isDeptRole = (role: ViewerRole) => role.startsWith('dept:');

export function ViewerRoleSelector({ selectedRoles, onRolesChange }: ViewerRoleSelectorProps) {
  // A viewer holds one functional role plus an optional department role.
  const currentRole: ViewerRole = selectedRoles.find(r => !isDeptRole(r)) || 'primary_care';
  const currentDept: ViewerRole | '' = selectedRoles.find(isDeptRole) || '';

  const emit = (role: ViewerRole, dept: ViewerRole | '') => {
    onRolesChange(dept ? [role, dept] : [role]);
  };

  return (
    <div className="flex items-center gap-2 text-sm flex-wrap">
      <span className="text-slate-500 font-medium mr-1">Security Clearance:</span>
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
                onChange={() => emit(role.value, currentDept)}
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
              <span>{role.label}</span>
            </label>
          );
        })}
      </div>
      <label className="inline-flex items-center gap-1.5 ml-1">
        <span className="text-slate-500 font-medium">Dept:</span>
        <select
          value={currentDept}
          onChange={e => emit(currentRole, e.target.value as ViewerRole | '')}
          className="px-2 py-1 rounded-md border border-slate-200 text-xs font-medium text-slate-700 bg-white"
        >
          <option value="">— None —</option>
          {DEPARTMENTS.map(dept => (
            <option key={dept.value} value={dept.value}>{dept.label}</option>
          ))}
        </select>
      </label>
    </div>
  );
}
