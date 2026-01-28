import { useState, useEffect } from 'react';
import { Search, Filter, ChevronDown, ChevronUp } from 'lucide-react';
import type { Patient } from '../lib/api';
import { searchPatients, fetchAllPatients, fetchResourceCounts } from '../lib/api';

interface PatientSidebarProps {
  onSelectPatient: (patient: Patient) => void;
  selectedPatientId?: string;
}

// Define available FHIR resource types for filtering
const RESOURCE_TYPE_OPTIONS = [
  { value: 'encounter', label: 'Encounter' },
  { value: 'medication', label: 'Medication' },
  { value: 'observation', label: 'Observation' },
  { value: 'condition', label: 'Condition' },
  { value: 'diagnostic_report', label: 'Report' },
  { value: 'procedure', label: 'Procedure' },
  { value: 'immunization', label: 'Immunization' },
  { value: 'imaging_study', label: 'Imaging' },
];

export function PatientSidebar({ onSelectPatient, selectedPatientId }: PatientSidebarProps) {
  const [patients, setPatients] = useState<Patient[]>([]);
  // We can treat `patients` as the source of truth for display
  // filtering happens at API level now
  const [searchTerm, setSearchTerm] = useState('');
  const [loading, setLoading] = useState(true);
  const [selectedResourceTypes, setSelectedResourceTypes] = useState<string[]>([]);
  const [showFilters, setShowFilters] = useState(false);
  const [resourceCounts, setResourceCounts] = useState<Record<string, number>>({});

  // Initial load
  useEffect(() => {
    loadPatients();
    loadResourceCounts();
  }, []);

  const loadResourceCounts = async () => {
    try {
      const counts = await fetchResourceCounts();
      const countMap: Record<string, number> = {};
      counts.forEach(c => {
        countMap[c.resourceType] = c.patientCount;
      });
      setResourceCounts(countMap);
    } catch (e) {
      console.error('Failed to load resource counts:', e);
    }
  };

  // Debounced Search with resource type filtering
  useEffect(() => {
    const timer = setTimeout(() => {
        if (searchTerm.trim() !== '' || selectedResourceTypes.length > 0) {
            performSearch(searchTerm);
        } else {
            // Reload all patients if search cleared and no filters
            // Optimization: could cache "all patients" in a separate state
            loadPatients();
        }
    }, 500);

    return () => clearTimeout(timer);
  }, [searchTerm, selectedResourceTypes]);

  const loadPatients = async () => {
    try {
      setLoading(true);
      const data = await fetchAllPatients();
      setPatients(data);
    } catch (e) {
      console.error('Failed to load patients:', e);
    } finally {
      setLoading(false);
    }
  };

  const performSearch = async (query: string) => {
    try {
        setLoading(true);
        const data = await searchPatients(query, selectedResourceTypes);
        setPatients(data);
    } catch (e) {
        console.error('Failed to search patients:', e);
    } finally {
        setLoading(false);
    }
  };

  const toggleResourceType = (resourceType: string) => {
    setSelectedResourceTypes(prev => {
      if (prev.includes(resourceType)) {
        return prev.filter(rt => rt !== resourceType);
      } else {
        return [...prev, resourceType];
      }
    });
  };

  // Removed applyFilters (client side), use patients directly
  const filteredPatients = patients;

  const calculateAge = (birthDate: string) => {
    const today = new Date();
    const birth = new Date(birthDate);
    let age = today.getFullYear() - birth.getFullYear();
    const monthDiff = today.getMonth() - birth.getMonth();
    if (monthDiff < 0 || (monthDiff === 0 && today.getDate() < birth.getDate())) {
      age--;
    }
    return age;
  };

  return (
    <div className="h-full flex flex-col bg-white border-l border-slate-200">
      <div className="flex-none p-4 border-b border-slate-200 bg-slate-50">
        <h2 className="text-lg font-bold text-slate-900 mb-3">Patients</h2>
        <div className="relative mb-3">
          <Search className="absolute left-3 top-1/2 transform -translate-y-1/2 text-slate-400 w-4 h-4" />
          <input
            type="text"
            placeholder="Search by ID or name..."
            value={searchTerm}
            onChange={(e) => setSearchTerm(e.target.value)}
            className="w-full pl-9 pr-3 py-2 text-sm rounded-lg border border-slate-300 bg-white text-slate-900 placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
          />
        </div>

        {/* Resource Type Filter Toggle */}
        <button
          onClick={() => setShowFilters(!showFilters)}
          className="w-full flex items-center justify-between px-3 py-2 text-sm bg-white border border-slate-300 rounded-lg hover:bg-slate-50 transition-colors"
        >
          <div className="flex items-center gap-2">
            <Filter className="w-4 h-4 text-slate-500" />
            <span className="font-medium text-slate-700">Filter by Resource Type</span>
            {selectedResourceTypes.length > 0 && (
              <span className="px-2 py-0.5 bg-blue-100 text-blue-700 text-xs font-semibold rounded-full">
                {selectedResourceTypes.length}
              </span>
            )}
          </div>
          {showFilters ? <ChevronUp className="w-4 h-4 text-slate-500" /> : <ChevronDown className="w-4 h-4 text-slate-500" />}
        </button>

        {/* Resource Type Checkboxes */}
        {showFilters && (
          <div className="mt-3 p-3 bg-white border border-slate-200 rounded-lg max-h-64 overflow-y-auto">
            <div className="space-y-2">
              {RESOURCE_TYPE_OPTIONS.map(option => (
                <label key={option.value} className="flex items-center justify-between cursor-pointer hover:bg-slate-50 px-2 py-1 rounded">
                  <div className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={selectedResourceTypes.includes(option.value)}
                      onChange={() => toggleResourceType(option.value)}
                      className="w-4 h-4 text-blue-600 border-slate-300 rounded focus:ring-blue-500"
                    />
                    <span className="text-sm text-slate-700">{option.label}</span>
                  </div>
                  <span className="text-xs text-slate-500">({resourceCounts[option.value] ?? '-'})</span>
                </label>
              ))}
            </div>
            {selectedResourceTypes.length > 0 && (
              <button
                onClick={() => setSelectedResourceTypes([])}
                className="mt-3 w-full text-xs text-blue-600 hover:text-blue-700 font-medium"
              >
                Clear all filters
              </button>
            )}
          </div>
        )}
      </div>

      <div className="flex-none px-4 py-2 bg-white border-b border-slate-200">
        <p className="text-xs font-medium text-slate-600">{filteredPatients.length} patients</p>
      </div>

      <div className="flex-1 overflow-y-auto">
        {loading ? (
          <div className="flex items-center justify-center py-12">
            <div className="animate-spin rounded-full h-8 w-8 border-3 border-blue-600 border-t-transparent"></div>
          </div>
        ) : filteredPatients.length === 0 ? (
          <div className="text-center py-12 px-4 text-slate-500">
            <p className="text-xs">No patients found</p>
          </div>
        ) : (
          <div className="divide-y divide-slate-200">
            {filteredPatients.map((patient) => {
              const isSelected = selectedPatientId === patient.id;

              return (
                <button
                  key={patient.id}
                  onClick={() => onSelectPatient(patient)}
                  className={`w-full text-left px-4 py-3 hover:bg-blue-50 transition-colors ${
                    isSelected ? 'bg-blue-50 border-l-4 border-blue-600' : ''
                  }`}
                >
                  <div className="flex items-start justify-between mb-1.5">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="px-1.5 py-0.5 bg-slate-100 text-slate-700 text-xs font-mono font-semibold rounded">
                          {patient.patient_identifier}
                        </span>
                      </div>
                      <h3 className="text-sm font-bold text-slate-900 mb-0.5">
                        {patient.family_name} {patient.given_name}
                      </h3>
                      <p className="text-xs text-slate-600 mb-1.5">
                        {calculateAge(patient.birth_date)} years old ・ {patient.gender === 'male' ? 'Male' : 'Female'}
                      </p>
                      {patient.snippet && (
                        <div 
                            className="mt-1 text-xs bg-yellow-50 text-slate-700 p-1.5 rounded border border-yellow-200 line-clamp-2"
                            dangerouslySetInnerHTML={{ __html: patient.snippet }}
                        />
                      )}
                    </div>
                  </div>
                </button>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
