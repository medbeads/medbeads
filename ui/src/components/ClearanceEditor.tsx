import { useState, useEffect } from 'react';
import { ShieldAlert, Plus, Trash2, Clock, AlertTriangle } from 'lucide-react';
import {
  fetchClearanceRules,
  createClearanceRule,
  deleteClearanceRule,
  VIEWER_ROLES,
  type ClearanceRule,
  type ViewerRole,
} from '../lib/api';

interface ClearanceEditorProps {
  beadId: string;
  beadType: string;
}

export function ClearanceEditor({ beadId, beadType }: ClearanceEditorProps) {
  const [rules, setRules] = useState<ClearanceRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [isAdding, setIsAdding] = useState(false);
  const [selectedRoles, setSelectedRoles] = useState<ViewerRole[]>([]);
  const [reason, setReason] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    loadRules();
  }, [beadId]);

  const loadRules = async () => {
    try {
      setLoading(true);
      const fetchedRules = await fetchClearanceRules(beadId);
      setRules(fetchedRules);
    } catch (error) {
      console.error('Failed to load clearance rules:', error);
    } finally {
      setLoading(false);
    }
  };

  const handleAddRule = async () => {
    if (selectedRoles.length === 0) return;

    try {
      setSaving(true);
      const newRule = await createClearanceRule({
        bead_id: beadId,
        denied_roles: selectedRoles,
        reason: reason || undefined,
        expires_at: expiresAt || null,
      });
      setRules([...rules, newRule]);
      setSelectedRoles([]);
      setReason('');
      setExpiresAt('');
      setIsAdding(false);
    } catch (error) {
      console.error('Failed to create clearance rule:', error);
    } finally {
      setSaving(false);
    }
  };

  const handleDeleteRule = async (ruleId: string) => {
    try {
      await deleteClearanceRule(ruleId);
      setRules(rules.filter(r => r.id !== ruleId));
    } catch (error) {
      console.error('Failed to delete clearance rule:', error);
    }
  };

  const toggleRole = (role: ViewerRole) => {
    if (selectedRoles.includes(role)) {
      setSelectedRoles(selectedRoles.filter(r => r !== role));
    } else {
      setSelectedRoles([...selectedRoles, role]);
    }
  };

  const getRoleLabel = (role: ViewerRole) => {
    const found = VIEWER_ROLES.find(r => r.value === role);
    return found ? found.labelJa : role;
  };

  // Don't show clearance for emergency/system bypassed items
  const restrictableRoles = VIEWER_ROLES.filter(
    r => r.value !== 'emergency' && r.value !== 'system'
  );

  return (
    <div className="bg-gradient-to-br from-slate-50 to-slate-100 border-2 border-slate-200 rounded-xl p-5">
      <div className="flex items-center gap-3 mb-4">
        <div className="w-10 h-10 bg-gradient-to-br from-slate-600 to-slate-700 rounded-lg flex items-center justify-center">
          <ShieldAlert className="w-5 h-5 text-white" />
        </div>
        <div>
          <h3 className="text-lg font-bold text-slate-900">Access Control</h3>
          <p className="text-xs text-slate-500">Manage who can view this {beadType}</p>
        </div>
      </div>

      {loading ? (
        <div className="flex items-center justify-center py-8">
          <div className="animate-spin rounded-full h-6 w-6 border-2 border-slate-400 border-t-transparent"></div>
        </div>
      ) : (
        <>
          {/* Existing Rules */}
          {rules.length > 0 && (
            <div className="space-y-2 mb-4">
              {rules.map(rule => (
                <div
                  key={rule.id}
                  className="flex items-center justify-between p-3 bg-white rounded-lg border border-slate-200"
                >
                  <div className="flex-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      {rule.denied_roles.map(role => (
                        <span
                          key={role}
                          className="px-2 py-0.5 bg-red-100 text-red-700 rounded text-xs font-medium"
                        >
                          {getRoleLabel(role)}
                        </span>
                      ))}
                      {rule.expires_at && (
                        <span className="flex items-center gap-1 px-2 py-0.5 bg-amber-100 text-amber-700 rounded text-xs font-medium">
                          <Clock className="w-3 h-3" />
                          {new Date(rule.expires_at).toLocaleDateString()}
                        </span>
                      )}
                    </div>
                    {rule.reason && (
                      <p className="text-xs text-slate-500 mt-1">{rule.reason}</p>
                    )}
                  </div>
                  <button
                    onClick={() => handleDeleteRule(rule.id)}
                    className="p-1.5 text-slate-400 hover:text-red-500 hover:bg-red-50 rounded transition-colors"
                    title="Remove restriction"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              ))}
            </div>
          )}

          {rules.length === 0 && !isAdding && (
            <div className="text-center py-6 text-slate-500">
              <AlertTriangle className="w-8 h-8 mx-auto mb-2 text-slate-300" />
              <p className="text-sm">No access restrictions</p>
              <p className="text-xs mt-1">This record is visible to all roles</p>
            </div>
          )}

          {/* Add New Rule */}
          {isAdding ? (
            <div className="p-4 bg-white rounded-lg border-2 border-blue-200">
              <h4 className="text-sm font-semibold text-slate-700 mb-3">
                Add Access Restriction
              </h4>

              <div className="mb-4">
                <label className="text-xs font-medium text-slate-600 mb-2 block">
                  Block access for:
                </label>
                <div className="flex flex-wrap gap-2">
                  {restrictableRoles.map(role => (
                    <button
                      key={role.value}
                      onClick={() => toggleRole(role.value)}
                      className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors ${
                        selectedRoles.includes(role.value)
                          ? 'bg-red-500 text-white'
                          : 'bg-slate-100 text-slate-600 hover:bg-slate-200'
                      }`}
                    >
                      {role.labelJa}
                    </button>
                  ))}
                </div>
              </div>

              <div className="mb-4">
                <label className="text-xs font-medium text-slate-600 mb-1 block">
                  Reason (optional)
                </label>
                <input
                  type="text"
                  value={reason}
                  onChange={e => setReason(e.target.value)}
                  placeholder="e.g., Patient requested privacy"
                  className="w-full px-3 py-2 text-sm border border-slate-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>

              <div className="mb-4">
                <label className="text-xs font-medium text-slate-600 mb-1 block">
                  Expires (optional)
                </label>
                <input
                  type="datetime-local"
                  value={expiresAt}
                  onChange={e => setExpiresAt(e.target.value)}
                  className="w-full px-3 py-2 text-sm border border-slate-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>

              <div className="flex gap-2">
                <button
                  onClick={handleAddRule}
                  disabled={selectedRoles.length === 0 || saving}
                  className="flex-1 px-4 py-2 bg-blue-600 text-white text-sm font-medium rounded-lg hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                >
                  {saving ? 'Saving...' : 'Save Restriction'}
                </button>
                <button
                  onClick={() => {
                    setIsAdding(false);
                    setSelectedRoles([]);
                    setReason('');
                    setExpiresAt('');
                  }}
                  className="px-4 py-2 bg-slate-100 text-slate-600 text-sm font-medium rounded-lg hover:bg-slate-200 transition-colors"
                >
                  Cancel
                </button>
              </div>
            </div>
          ) : (
            <button
              onClick={() => setIsAdding(true)}
              className="w-full flex items-center justify-center gap-2 px-4 py-2.5 border-2 border-dashed border-slate-300 rounded-lg text-slate-600 hover:border-slate-400 hover:bg-slate-50 transition-colors"
            >
              <Plus className="w-4 h-4" />
              <span className="text-sm font-medium">Add Access Restriction</span>
            </button>
          )}
        </>
      )}
    </div>
  );
}
