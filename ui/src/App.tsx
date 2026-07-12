import { useState, useEffect, useCallback, useMemo } from 'react';
import { Activity, Pill, FileText, AlertCircle, Stethoscope, User, LayoutList, Network, Maximize2, Minimize2, Printer } from 'lucide-react';
import { fetchPatientTimeline, fetchPatientGraph, setViewerRoles, getViewerRoles } from './lib/api';
import type { Patient, TimelineItem, ViewerRole, PatientGraph } from './lib/api';
import { TimelineCard } from './components/TimelineCard';
import { DetailPanel } from './components/DetailPanel';
import { PatientSidebar } from './components/PatientSidebar';
import GraphView from './components/GraphView';
import { ViewerRoleSelector } from './components/ViewerRoleSelector';

type TabType = 'timeline' | 'medications' | 'observations' | 'reports' | 'conditions';

function App() {
  const [selectedPatient, setSelectedPatient] = useState<Patient | null>(null);
  const [timelineItems, setTimelineItems] = useState<TimelineItem[]>([]);
  const [selectedItem, setSelectedItem] = useState<TimelineItem | null>(null);
  const [loading, setLoading] = useState(false);
  const [activeTab, setActiveTab] = useState<TabType>('timeline');
  const [viewMode, setViewMode] = useState<'list' | 'graph'>('list');
  // Manual 1-column/2-column toggle for the view body (independent of
  // viewMode — usable from both list and graph, not auto-driven by graph
  // mode), triggered by the expand/collapse button in the view panel's
  // header bar. Session-only; no persistence required.
  const [wideView, setWideView] = useState(false);
  // "Figure mode" (paper-figure capture): opt-in, only meaningful in Graph
  // View. Renders larger text-labeled Bead tiles and boosted-opacity/width
  // clinical_link arcs so a static screenshot is legible without hovering —
  // see GraphView.tsx's `figureMode` prop. Session-only; no persistence
  // required (same convention as `wideView`).
  const [figureMode, setFigureMode] = useState(false);
  const [viewerRoles, setViewerRolesState] = useState<ViewerRole[]>(getViewerRoles());
  const [patientGraph, setPatientGraph] = useState<PatientGraph | null>(null);
  const [graphLoading, setGraphLoading] = useState(false);
  const [graphError, setGraphError] = useState<string | null>(null);

  const handleViewerRolesChange = useCallback((roles: ViewerRole[]) => {
    setViewerRolesState(roles);
    setViewerRoles(roles);
    // Re-fetch data with new roles
    if (selectedPatient) {
      fetchTimelineData();
      if (viewMode === 'graph') {
        fetchGraphData();
      }
    }
  }, [selectedPatient, viewMode]);

  useEffect(() => {
    if (selectedPatient) {
      fetchTimelineData();
    }
  }, [selectedPatient?.id]);

  // Graph data (R7) is fetched lazily: only once the user actually switches
  // to Graph View, and again whenever the selected patient changes while
  // already in Graph View. Avoids an extra request per patient click when
  // the user never opens the graph.
  useEffect(() => {
    if (selectedPatient && viewMode === 'graph') {
      fetchGraphData();
    }
  }, [selectedPatient?.id, viewMode]);

  // restrictedIds is derived synchronously from data the timeline fetch
  // already returned (each item's `restricted` flag, itself derived from the
  // bead's `_restricted` marker set server-side by clearance.FilterByAccess
  // for the current viewer's roles). No follow-up request per bead is ever
  // made — see R8b: this replaces an O(N) `/clearance` fan-out (~1 request
  // per bead) that blew the server's rate limit on large patients.
  const restrictedIds = useMemo(() => {
    const ids = new Set<string>();
    for (const item of timelineItems) {
      if (item.data?.id && item.restricted) {
        ids.add(item.data.id);
      }
    }
    return ids;
  }, [timelineItems]);

  async function fetchTimelineData() {
    if (!selectedPatient) return;

    try {
      setLoading(true);
      const items = await fetchPatientTimeline(selectedPatient.id);
      setTimelineItems(items);
    } catch (error) {
      console.error('Error fetching timeline data:', error);
    } finally {
      setLoading(false);
    }
  }

  async function fetchGraphData() {
    if (!selectedPatient) return;

    try {
      setGraphLoading(true);
      setGraphError(null);
      const graph = await fetchPatientGraph(selectedPatient.id);
      setPatientGraph(graph);
    } catch (error) {
      console.error('Error fetching patient graph:', error);
      setPatientGraph(null);
      setGraphError('Failed to load the bead graph for this patient.');
    } finally {
      setGraphLoading(false);
    }
  }

  const getFilteredItems = () => {
    switch (activeTab) {
      case 'medications':
        return timelineItems.filter(item => item.type === 'medication');
      case 'observations':
        return timelineItems.filter(item => item.type === 'observation');
      case 'reports':
        return timelineItems.filter(item => item.type === 'diagnostic_report');
      case 'conditions':
        return timelineItems.filter(item => item.type === 'condition');
      default:
        return timelineItems;
    }
  };

  const filteredItems = getFilteredItems();

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
    <div className="h-screen flex overflow-hidden bg-slate-50">
      <div className="flex-1 flex flex-col overflow-hidden">
        <div className="flex-none bg-white border-b border-slate-200 shadow-sm">
          <div className="px-6 py-4 flex items-center justify-between">
            <h1 className="text-2xl font-bold text-slate-900">MedBeads Patient Overview</h1>
            <ViewerRoleSelector
              selectedRoles={viewerRoles}
              onRolesChange={handleViewerRolesChange}
            />
          </div>
        </div>

        {selectedPatient ? (
          <>
            <div className="flex-none bg-gradient-to-r from-blue-600 to-blue-700 text-white px-6 py-6 shadow-lg">
              <div className="flex items-start justify-between">
                <div>
                  <div className="flex items-center gap-4 mb-2">
                    <span className="px-3 py-1 bg-white/20 backdrop-blur-sm text-white text-sm font-mono font-semibold rounded-lg">
                      {selectedPatient.patient_identifier}
                    </span>
                    <h2 className="text-3xl font-bold">
                      {selectedPatient.family_name} {selectedPatient.given_name}
                    </h2>
                  </div>
                  <p className="text-white/90 text-lg">
                    {new Date(selectedPatient.birth_date).toLocaleDateString('en-US')} (
                    {calculateAge(selectedPatient.birth_date)} years old) ・{' '}
                    {selectedPatient.gender === 'male' ? 'Male' : 'Female'}
                  </p>
                </div>
              </div>
            </div>

            <div className="flex-none bg-white border-b border-slate-200">
              <div className="px-6">
                <div className="flex items-center gap-1 overflow-x-auto">
                  {[
                    { key: 'timeline', label: 'Timeline', icon: Activity },
                    { key: 'medications', label: 'Medications', icon: Pill },
                    { key: 'observations', label: 'Observations', icon: Stethoscope },
                    { key: 'reports', label: 'Reports', icon: FileText },
                    { key: 'conditions', label: 'Conditions', icon: AlertCircle },
                  ].map(({ key, label, icon: Icon }) => (
                    <button
                      key={key}
                      onClick={() => setActiveTab(key as TabType)}
                      className={`flex items-center gap-2 px-4 py-3 font-medium text-sm border-b-2 transition-colors whitespace-nowrap ${
                        activeTab === key
                          ? 'border-blue-600 text-blue-600 bg-blue-50'
                          : 'border-transparent text-slate-600 hover:text-slate-900 hover:bg-slate-50'
                      }`}
                    >
                      <Icon className="w-4 h-4" />
                      {label}
                    </button>
                  ))}
                </div>
              </div>
            </div>

            <div
              className={`flex-1 grid gap-6 p-6 overflow-hidden relative ${
                wideView ? 'grid-cols-1' : 'grid-cols-2'
              }`}
            >
              <div className="bg-white rounded-xl shadow-lg border border-slate-200 overflow-hidden flex flex-col h-full">
                <div
                  className="flex-none bg-gradient-to-r from-slate-700 to-slate-600 px-6 py-4 flex items-center justify-between cursor-pointer"
                  onClick={() => setWideView((v) => !v)}
                  role="button"
                  tabIndex={0}
                  aria-label={wideView ? '2カラムに戻す' : '全幅表示'}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      setWideView((v) => !v);
                    }
                  }}
                >
                  <h2 className="text-xl font-bold text-white">
                    {activeTab === 'timeline' && 'Timeline'}
                    {activeTab === 'medications' && 'Medications'}
                    {activeTab === 'observations' && 'Observations'}
                    {activeTab === 'reports' && 'Reports'}
                    {activeTab === 'conditions' && 'Conditions'}
                  </h2>

                  <div className="flex items-center gap-4">
                    <div className="flex bg-slate-800/50 rounded-lg p-1" onClick={(e) => e.stopPropagation()}>
                        <button
                            onClick={() => setViewMode('list')}
                            className={`p-1.5 rounded-md transition-all ${viewMode === 'list' ? 'bg-white text-slate-800 shadow-sm' : 'text-slate-300 hover:text-white'}`}
                            title="List View"
                        >
                            <LayoutList size={18} />
                        </button>
                        <button
                            onClick={() => setViewMode('graph')}
                            className={`p-1.5 rounded-md transition-all ${viewMode === 'graph' ? 'bg-white text-slate-800 shadow-sm' : 'text-slate-300 hover:text-white'}`}
                            title="Graph View"
                        >
                            <Network size={18} />
                        </button>
                        {viewMode === 'graph' && (
                            <button
                                onClick={() => setFigureMode((v) => !v)}
                                className={`p-1.5 rounded-md transition-all ${figureMode ? 'bg-white text-slate-800 shadow-sm' : 'text-slate-300 hover:text-white'}`}
                                title="Figure Mode"
                                aria-pressed={figureMode}
                            >
                                <Printer size={18} />
                            </button>
                        )}
                    </div>
                    <span className="px-3 py-1 bg-white/20 text-white text-sm font-semibold rounded-lg">
                        {viewMode === 'graph' ? `${patientGraph?.beads.length ?? 0} beads` : `${filteredItems.length} items`}
                    </span>
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        setWideView((v) => !v);
                      }}
                      className="p-1.5 rounded-md text-slate-300 hover:text-white hover:bg-slate-800/50 transition-all"
                      title={wideView ? '2カラムに戻す' : '全幅表示'}
                      aria-label={wideView ? '2カラムに戻す' : '全幅表示'}
                    >
                      {wideView ? <Minimize2 size={18} /> : <Maximize2 size={18} />}
                    </button>
                  </div>
                </div>

                <div className="flex-1 overflow-hidden relative">
                  {viewMode === 'graph' ? (
                    graphLoading ? (
                      <div className="flex items-center justify-center py-16 h-full">
                        <div className="animate-spin rounded-full h-12 w-12 border-4 border-blue-600 border-t-transparent"></div>
                      </div>
                    ) : graphError ? (
                      <div className="text-center py-16 text-slate-500 h-full flex flex-col items-center justify-center">
                        <AlertCircle className="w-16 h-16 mx-auto mb-4 text-red-300" />
                        <p className="text-lg font-medium text-red-600">{graphError}</p>
                        <button
                          onClick={() => fetchGraphData()}
                          className="mt-4 px-4 py-2 text-sm bg-blue-600 text-white rounded-lg hover:bg-blue-700"
                        >
                          Retry
                        </button>
                      </div>
                    ) : patientGraph && patientGraph.beads.length > 0 ? (
                      <GraphView
                        graph={patientGraph}
                        onBeadClick={(bead) => {
                          const item = timelineItems.find((i) => i.data?.id === bead.id);
                          if (item) setSelectedItem(item);
                        }}
                        selectedBeadId={selectedItem?.data?.id}
                        figureMode={figureMode}
                      />
                    ) : (
                      <div className="text-center py-16 text-slate-500 h-full flex flex-col items-center justify-center">
                        <Network className="w-16 h-16 mx-auto mb-4 text-slate-300" />
                        <p className="text-lg font-medium">No graph data available</p>
                        <p className="text-sm mt-2">No beads found for this patient</p>
                      </div>
                    )
                  ) : loading ? (
                    <div className="flex items-center justify-center py-16 h-full">
                      <div className="animate-spin rounded-full h-12 w-12 border-4 border-blue-600 border-t-transparent"></div>
                    </div>
                  ) : filteredItems.length === 0 ? (
                    <div className="text-center py-16 text-slate-500 h-full flex flex-col items-center justify-center">
                      <Activity className="w-16 h-16 mx-auto mb-4 text-slate-300" />
                      <p className="text-lg font-medium">No data available</p>
                      <p className="text-sm mt-2">No records found for this patient</p>
                    </div>
                  ) : (
                    <div className="overflow-y-auto h-full p-6 space-y-4">
                        {filteredItems.map((item, index) => (
                            <TimelineCard
                            key={`${item.type}-${item.data?.id || index}`}
                            item={item}
                            isSelected={selectedItem === item}
                            onClick={() => setSelectedItem(item)}
                            restricted={item.restricted}
                            />
                        ))}
                    </div>
                  )}
                </div>
              </div>

              {wideView ? (
                // wideView (user-toggled via the header bar's expand/collapse
                // button — independent of viewMode, useful for both list and
                // graph) drops to 1 column so the view body gets full width;
                // Details & AI Insights renders as a floating overlay instead
                // of a permanent 2nd column — only shown once a Bead/item is
                // actually selected, and dismissible so it never permanently
                // eats the reclaimed width.
                selectedItem && (
                  <div className="absolute top-0 right-0 h-full w-full max-w-md z-20 p-3">
                    <div className="bg-white rounded-xl shadow-2xl border border-slate-200 overflow-hidden flex flex-col h-full">
                      <div className="flex-none bg-gradient-to-r from-cyan-700 to-cyan-600 px-6 py-4 flex items-center justify-between">
                        <h2 className="text-xl font-bold text-white">Details & AI Insights</h2>
                        <button
                          onClick={() => setSelectedItem(null)}
                          className="text-white/80 hover:text-white text-sm font-medium"
                          aria-label="Close details panel"
                        >
                          ✕
                        </button>
                      </div>
                      <div className="flex-1 overflow-y-auto">
                        <DetailPanel selectedItem={selectedItem} patient={selectedPatient} restrictedIds={restrictedIds} />
                      </div>
                    </div>
                  </div>
                )
              ) : (
                <div className="bg-white rounded-xl shadow-lg border border-slate-200 overflow-hidden flex flex-col">
                  <div className="flex-none bg-gradient-to-r from-cyan-700 to-cyan-600 px-6 py-4">
                    <h2 className="text-xl font-bold text-white">Details & AI Insights</h2>
                  </div>
                  <div className="flex-1 overflow-y-auto">
                    {selectedItem ? (
                      <DetailPanel selectedItem={selectedItem} patient={selectedPatient} restrictedIds={restrictedIds} />
                    ) : (
                      <div className="flex items-center justify-center h-full p-6 text-slate-500">
                        <div className="text-center">
                          <FileText className="w-16 h-16 mx-auto mb-4 text-slate-300" />
                          <p className="text-lg font-medium">Select an item</p>
                          <p className="text-sm mt-2">Click on an item from the list to view details</p>
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          </>
        ) : (
          <div className="flex-1 flex items-center justify-center p-6">
            <div className="text-center text-slate-500">
              <User className="w-24 h-24 mx-auto mb-6 text-slate-300" />
              <p className="text-2xl font-medium mb-2">Select a Patient</p>
              <p className="text-lg">Choose a patient from the sidebar to view their medical records</p>
            </div>
          </div>
        )}
      </div>

      <div className="w-96 flex-none">
        <PatientSidebar
          onSelectPatient={setSelectedPatient}
          selectedPatientId={selectedPatient?.id}
        />
      </div>
    </div>
  );
}

export default App;
