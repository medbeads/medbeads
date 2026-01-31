import { useState } from 'react';
import { Eye, ChevronDown, Shield } from 'lucide-react';
import { VIEWER_ROLES, type ViewerRole } from '../lib/api';

interface ViewerRoleSelectorProps {
  selectedRoles: ViewerRole[];
  onRolesChange: (roles: ViewerRole[]) => void;
}

export function ViewerRoleSelector({ selectedRoles, onRolesChange }: ViewerRoleSelectorProps) {
  const [isOpen, setIsOpen] = useState(false);

  const selectSingleRole = (role: ViewerRole) => {
    onRolesChange([role]);
    setIsOpen(false);
  };

  const getRoleLabel = (role: ViewerRole) => {
    const found = VIEWER_ROLES.find(r => r.value === role);
    return found ? found.labelJa : role;
  };

  const isEmergencyOrSystem = selectedRoles.includes('emergency') || selectedRoles.includes('system');

  return (
    <div className="relative">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className={`flex items-center gap-2 px-3 py-2 rounded-lg border-2 transition-all ${
          isEmergencyOrSystem
            ? 'bg-amber-50 border-amber-300 text-amber-800'
            : 'bg-white border-slate-200 text-slate-700 hover:border-slate-300'
        }`}
      >
        <Eye className="w-4 h-4" />
        <span className="text-sm font-medium">
          {selectedRoles.length === 1
            ? getRoleLabel(selectedRoles[0])
            : `${selectedRoles.length} roles`}
        </span>
        <ChevronDown className={`w-4 h-4 transition-transform ${isOpen ? 'rotate-180' : ''}`} />
      </button>

      {isOpen && (
        <>
          <div
            className="fixed inset-0 z-10"
            onClick={() => setIsOpen(false)}
          />
          <div className="absolute right-0 mt-2 w-64 bg-white rounded-xl shadow-xl border border-slate-200 z-20 overflow-hidden">
            <div className="p-3 bg-slate-50 border-b border-slate-200">
              <div className="flex items-center gap-2 text-sm font-semibold text-slate-700">
                <Shield className="w-4 h-4" />
                <span>Viewer Role (Demo)</span>
              </div>
              <p className="text-xs text-slate-500 mt-1">
                Select a role to simulate different access levels
              </p>
            </div>

            <div className="max-h-64 overflow-y-auto">
              {VIEWER_ROLES.map(role => (
                <button
                  key={role.value}
                  onClick={() => selectSingleRole(role.value)}
                  className={`w-full flex items-center gap-3 px-4 py-2.5 text-left transition-colors ${
                    selectedRoles.includes(role.value)
                      ? 'bg-blue-50 text-blue-700'
                      : 'hover:bg-slate-50 text-slate-700'
                  }`}
                >
                  <div
                    className={`w-3 h-3 rounded-full border-2 ${
                      selectedRoles.includes(role.value)
                        ? 'bg-blue-500 border-blue-500'
                        : 'border-slate-300'
                    }`}
                  />
                  <div className="flex-1">
                    <div className="text-sm font-medium">{role.labelJa}</div>
                    <div className="text-xs text-slate-500">{role.label}</div>
                  </div>
                  {(role.value === 'emergency' || role.value === 'system') && (
                    <span className="text-xs px-1.5 py-0.5 bg-amber-100 text-amber-700 rounded">
                      Full Access
                    </span>
                  )}
                </button>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  );
}
