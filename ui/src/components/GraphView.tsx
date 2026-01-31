import { useMemo, useCallback } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  MarkerType,
  Position,
  ReactFlowProvider,
  type Node,
  type Edge
} from 'reactflow';
import 'reactflow/dist/style.css';
import { type TimelineItem, type ClearanceRule, type ViewerRole, getViewerRoles } from '../lib/api';

interface GraphViewProps {
  items: TimelineItem[];
  onNodeClick?: (item: TimelineItem) => void;
  selectedId?: string;
  clearanceRulesMap?: Record<string, ClearanceRule[]>;
}

const nodeWidth = 180;
const nodeHeight = 70;
const xGap = 30;
const yGap = 120;

// Define nodeTypes and edgeTypes outside component to avoid React Flow warning
const nodeTypes = {};
const edgeTypes = {};

// Check if a bead is restricted for the current viewer
const isRestrictedForViewer = (rules: ClearanceRule[] | undefined, viewerRoles: ViewerRole[]): boolean => {
  if (!rules || rules.length === 0) return false;

  // Emergency and system roles always have access
  if (viewerRoles.includes('emergency') || viewerRoles.includes('system')) {
    return false;
  }

  const now = new Date();

  for (const rule of rules) {
    // Check expiration
    if (rule.expires_at) {
      const expiresAt = new Date(rule.expires_at);
      if (now > expiresAt) continue;
    }

    // Check if any viewer role is denied
    for (const viewerRole of viewerRoles) {
      if (rule.denied_roles.includes(viewerRole)) {
        return true;
      }
    }
  }

  return false;
};

// Get the denied roles for display
const getDeniedRoles = (rules: ClearanceRule[] | undefined): string[] => {
  if (!rules || rules.length === 0) return [];

  const deniedSet = new Set<string>();
  const now = new Date();

  for (const rule of rules) {
    if (rule.expires_at) {
      const expiresAt = new Date(rule.expires_at);
      if (now > expiresAt) continue;
    }
    rule.denied_roles.forEach(r => deniedSet.add(r));
  }

  return Array.from(deniedSet);
};

// Custom layout: Group by Date (Y axis = Time, X axis = Items in same date)
const getLayoutedElements = (nodes: Node[], edges: Edge[], items: TimelineItem[]) => {
  // 1. Group nodes by date
  const nodesByDate: Record<string, Node[]> = {};

  nodes.forEach(node => {
    const item = items.find(i => i.data.id === node.id);
    if (item && item.date) {
        const dateKey = item.date.length >= 10 ? item.date.substring(0, 10) : 'unknown';
        if (!nodesByDate[dateKey]) {
            nodesByDate[dateKey] = [];
        }
        nodesByDate[dateKey].push(node);
    } else {
        if (!nodesByDate['unknown']) nodesByDate['unknown'] = [];
        nodesByDate['unknown'].push(node);
    }
  });

  // 2. Sort dates (Newest first -> Top)
  const sortedDates = Object.keys(nodesByDate).sort((a, b) => {
      if (a === 'unknown') return 1;
      if (b === 'unknown') return -1;
      return b.localeCompare(a);
  });

  // 3. Assign positions - Start from 0,0, no negative coordinates
  let currentY = 0;

  sortedDates.forEach(date => {
    const rowNodes = nodesByDate[date];

    // Sort nodes within row by type
    rowNodes.sort((a, b) => {
        const typeA = items.find(i => i.data.id === a.id)?.type || '';
        const typeB = items.find(i => i.data.id === b.id)?.type || '';
        return typeA.localeCompare(typeB);
    });

    // Position nodes starting from x=0
    rowNodes.forEach((node, index) => {
      node.position = {
        x: index * (nodeWidth + xGap),
        y: currentY
      };

      node.sourcePosition = Position.Bottom;
      node.targetPosition = Position.Top;
    });

    currentY += nodeHeight + yGap;
  });

  return { nodes, edges };
};

function GraphViewInner({ items, onNodeClick, selectedId, clearanceRulesMap = {} }: GraphViewProps) {
  const viewerRoles = getViewerRoles();

  const { nodes: initialNodes, edges: initialEdges } = useMemo(() => {
    const newNodes: Node[] = [];
    const newEdges: Edge[] = [];
    const idSet = new Set(items.map(item => item.data.id));

    items.forEach((item) => {
      const itemId = item.data.id;
      const isSelected = selectedId === itemId;
      const rules = clearanceRulesMap[itemId];
      const isRestricted = isRestrictedForViewer(rules, viewerRoles);
      const deniedRoles = getDeniedRoles(rules);
      const hasClearance = deniedRoles.length > 0;

      let label = item.type.toUpperCase();
      let bgColor = "rgba(255, 255, 255, 0.95)";
      let borderColor = "#cbd5e1";

      if (item.type === "medication") {
        label = "💊 Medication";
        bgColor = isSelected ? '#dbeafe' : '#f0fdf4';
        borderColor = isSelected ? '#2563eb' : '#16a34a';
      } else if (item.type === "observation") {
        label = "📈 Observation";
        bgColor = isSelected ? '#dbeafe' : '#f8fafc';
        borderColor = isSelected ? '#2563eb' : '#64748b';
      } else if (item.type === "encounter") {
        label = "🩺 Encounter";
        bgColor = isSelected ? '#dbeafe' : '#fff7ed';
        borderColor = isSelected ? '#2563eb' : '#f97316';
      } else if (item.type === "condition") {
        label = "⚠️ Condition";
        bgColor = isSelected ? '#dbeafe' : '#fef2f2';
        borderColor = isSelected ? '#2563eb' : '#dc2626';
      } else if (item.type === "diagnostic_report") {
        label = "📄 Report";
        bgColor = isSelected ? '#dbeafe' : '#f0f9ff';
        borderColor = isSelected ? '#2563eb' : '#0891b2';
      }

      let detail = "";
      if (item.type === "medication") detail = item.data.medication_name;
      if (item.type === "observation") detail = item.data.display_name;
      if (item.type === "encounter") detail = item.data.encounter_type;
      if (item.type === "condition") detail = item.data.condition_name;
      if (item.type === "diagnostic_report") detail = item.data.title;

      if (detail && detail.length > 20) detail = detail.substring(0, 18) + "...";

      // Add clearance indicator to label
      let clearanceLabel = '';
      if (hasClearance) {
        const roleStr = deniedRoles.slice(0, 2).join(', ');
        clearanceLabel = `\n🔒 ${roleStr}${deniedRoles.length > 2 ? '...' : ''}`;
      }

      // Apply restricted overlay style
      let overlayStyle = {};
      if (isRestricted) {
        // Red transparent overlay for restricted items
        bgColor = 'rgba(254, 202, 202, 0.9)'; // red-200 with opacity
        borderColor = '#ef4444'; // red-500
        overlayStyle = {
          backgroundImage: 'repeating-linear-gradient(45deg, transparent, transparent 5px, rgba(239, 68, 68, 0.1) 5px, rgba(239, 68, 68, 0.1) 10px)',
        };
      } else if (hasClearance) {
        // Yellow transparent overlay for items with clearance (but viewer has access)
        overlayStyle = {
          backgroundImage: 'linear-gradient(135deg, rgba(251, 191, 36, 0.15) 0%, transparent 50%)',
        };
      }

      newNodes.push({
        id: itemId,
        data: { label: `${label}\n${detail}\n${new Date(item.date).toLocaleDateString()}${clearanceLabel}` },
        position: { x: 0, y: 0 },
        style: {
            background: bgColor,
            border: `2px solid ${borderColor}`,
            color: isRestricted ? '#991b1b' : '#1e293b',
            borderRadius: '10px',
            fontSize: '11px',
            padding: '8px',
            width: nodeWidth,
            textAlign: 'center' as const,
            whiteSpace: 'pre-wrap' as const,
            fontWeight: '500',
            boxShadow: isSelected ? '0 0 0 3px rgba(37, 99, 235, 0.3)' : '0 1px 3px rgba(0,0,0,0.1)',
            cursor: 'pointer',
            opacity: isRestricted ? 0.7 : 1,
            ...overlayStyle,
        },
      });

      // Edges (Parent -> Child)
      item.parents.forEach((parentId) => {
        if (idSet.has(parentId)) {
            newEdges.push({
                id: `e-${parentId}-${itemId}`,
                source: parentId,
                target: itemId,
                type: 'smoothstep',
                animated: false,
                style: { stroke: '#94a3b8', strokeWidth: 1 },
                markerEnd: {
                    type: MarkerType.ArrowClosed,
                    color: '#94a3b8',
                    width: 15,
                    height: 15,
                },
            });
        }
      });
    });

    return getLayoutedElements(newNodes, newEdges, items);
  }, [items, selectedId, clearanceRulesMap, viewerRoles]);

  const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    const item = items.find(i => i.data.id === node.id);
    if (item && onNodeClick) onNodeClick(item);
  }, [items, onNodeClick]);

  return (
    <ReactFlow
      nodes={initialNodes}
      edges={initialEdges}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
      onNodeClick={handleNodeClick}
      fitView
      fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
      minZoom={0.05}
      maxZoom={2}
      attributionPosition="bottom-right"
      proOptions={{ hideAttribution: true }}
    >
      <Background color="#e2e8f0" gap={20} size={1} />
      <Controls showInteractive={false} className="bg-white border border-slate-200 shadow-sm" />
      <MiniMap
          nodeColor={(n) => {
              if (n.style?.background) return n.style.background as string;
              return '#fff';
          }}
          nodeBorderRadius={2}
          className="border border-slate-200 shadow-lg rounded-lg overflow-hidden"
      />

      {/* Legend for clearance colors */}
      <div className="absolute top-4 left-4 bg-white/90 backdrop-blur-sm rounded-lg shadow-md border border-slate-200 p-3 text-xs">
        <div className="font-semibold text-slate-700 mb-2">クリアランス凡例</div>
        <div className="space-y-1.5">
          <div className="flex items-center gap-2">
            <div className="w-4 h-4 rounded bg-amber-100 border border-amber-300" style={{ backgroundImage: 'linear-gradient(135deg, rgba(251, 191, 36, 0.3) 0%, transparent 50%)' }} />
            <span className="text-slate-600">制限あり（アクセス可）</span>
          </div>
          <div className="flex items-center gap-2">
            <div className="w-4 h-4 rounded bg-red-200 border border-red-400" style={{ backgroundImage: 'repeating-linear-gradient(45deg, transparent, transparent 2px, rgba(239, 68, 68, 0.2) 2px, rgba(239, 68, 68, 0.2) 4px)' }} />
            <span className="text-slate-600">アクセス不可</span>
          </div>
        </div>
      </div>
    </ReactFlow>
  );
}

export default function GraphView(props: GraphViewProps) {
  return (
    <div style={{ width: '100%', height: '100%' }} className="bg-slate-50">
      <ReactFlowProvider>
        <GraphViewInner {...props} />
      </ReactFlowProvider>
    </div>
  );
}
