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

// Get the denied roles for display (as a sorted string key for grouping)
const getDeniedRolesKey = (rules: ClearanceRule[] | undefined): string => {
  if (!rules || rules.length === 0) return '';

  const deniedSet = new Set<string>();
  const now = new Date();

  for (const rule of rules) {
    if (rule.expires_at) {
      const expiresAt = new Date(rule.expires_at);
      if (now > expiresAt) continue;
    }
    rule.denied_roles.forEach(r => deniedSet.add(r));
  }

  return Array.from(deniedSet).sort().join(',');
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

// Color palette for different clearance groups
const clearanceColors = [
  { bg: 'rgba(251, 191, 36, 0.15)', border: 'rgba(251, 191, 36, 0.5)' },   // amber
  { bg: 'rgba(239, 68, 68, 0.12)', border: 'rgba(239, 68, 68, 0.4)' },     // red
  { bg: 'rgba(168, 85, 247, 0.12)', border: 'rgba(168, 85, 247, 0.4)' },   // purple
  { bg: 'rgba(34, 197, 94, 0.12)', border: 'rgba(34, 197, 94, 0.4)' },     // green
  { bg: 'rgba(59, 130, 246, 0.12)', border: 'rgba(59, 130, 246, 0.4)' },   // blue
  { bg: 'rgba(236, 72, 153, 0.12)', border: 'rgba(236, 72, 153, 0.4)' },   // pink
];

// Custom layout: Group by Date (Y axis = Time, X axis = Items in same date)
const getLayoutedElements = (nodes: Node[], edges: Edge[], items: TimelineItem[]) => {
  // 1. Group nodes by date
  const nodesByDate: Record<string, Node[]> = {};

  nodes.forEach(node => {
    // Skip group nodes
    if (node.id.startsWith('clearance-group-')) return;

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

  const { nodes: initialNodes, edges: initialEdges, clearanceGroups } = useMemo(() => {
    const newNodes: Node[] = [];
    const newEdges: Edge[] = [];
    const idSet = new Set(items.map(item => item.data.id));

    // Track nodes by their clearance group
    const clearanceGroupNodes: Record<string, { nodeIds: string[], deniedRoles: string[] }> = {};

    items.forEach((item) => {
      const itemId = item.data.id;
      const isSelected = selectedId === itemId;
      const rules = clearanceRulesMap[itemId];
      const isRestricted = isRestrictedForViewer(rules, viewerRoles);
      const deniedRoles = getDeniedRoles(rules);
      const deniedRolesKey = getDeniedRolesKey(rules);
      const hasClearance = deniedRoles.length > 0;

      // Track clearance groups
      if (hasClearance && deniedRolesKey) {
        if (!clearanceGroupNodes[deniedRolesKey]) {
          clearanceGroupNodes[deniedRolesKey] = { nodeIds: [], deniedRoles };
        }
        clearanceGroupNodes[deniedRolesKey].nodeIds.push(itemId);
      }

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

      // Add small lock icon for nodes with clearance
      let clearanceIndicator = '';
      if (hasClearance) {
        clearanceIndicator = isRestricted ? ' 🔒' : ' 🔓';
      }

      newNodes.push({
        id: itemId,
        data: { label: `${label}${clearanceIndicator}\n${detail}\n${new Date(item.date).toLocaleDateString()}` },
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
            opacity: isRestricted ? 0.6 : 1,
            zIndex: 10,
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

    // Apply layout first to get positions
    const { nodes: layoutedNodes, edges: layoutedEdges } = getLayoutedElements(newNodes, newEdges, items);

    // Create clearance group rectangles after layout
    const groupNodes: Node[] = [];
    let colorIndex = 0;

    Object.entries(clearanceGroupNodes).forEach(([key, group]) => {
      if (group.nodeIds.length === 0) return;

      // Find bounding box for this group
      const groupItemNodes = layoutedNodes.filter(n => group.nodeIds.includes(n.id));
      if (groupItemNodes.length === 0) return;

      const padding = 15;
      let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;

      groupItemNodes.forEach(node => {
        minX = Math.min(minX, node.position.x);
        minY = Math.min(minY, node.position.y);
        maxX = Math.max(maxX, node.position.x + nodeWidth);
        maxY = Math.max(maxY, node.position.y + nodeHeight);
      });

      const color = clearanceColors[colorIndex % clearanceColors.length];
      colorIndex++;

      // Create background rectangle node
      groupNodes.push({
        id: `clearance-group-${key}`,
        data: {
          label: `🔒 ${group.deniedRoles.slice(0, 3).join(', ')}${group.deniedRoles.length > 3 ? '...' : ''}`,
        },
        position: { x: minX - padding, y: minY - padding - 20 },
        style: {
          width: maxX - minX + padding * 2,
          height: maxY - minY + padding * 2 + 20,
          background: color.bg,
          border: `2px dashed ${color.border}`,
          borderRadius: '12px',
          zIndex: -1,
          pointerEvents: 'none' as const,
          fontSize: '10px',
          fontWeight: '600',
          color: color.border.replace('0.4', '1').replace('0.5', '1'),
          padding: '4px 8px',
          display: 'flex',
          alignItems: 'flex-start',
          justifyContent: 'flex-start',
        },
        selectable: false,
        draggable: false,
      });
    });

    // Add group nodes at the beginning (so they render behind)
    const allNodes = [...groupNodes, ...layoutedNodes];

    return {
      nodes: allNodes,
      edges: layoutedEdges,
      clearanceGroups: Object.keys(clearanceGroupNodes)
    };
  }, [items, selectedId, clearanceRulesMap, viewerRoles]);

  const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    // Ignore clicks on group nodes
    if (node.id.startsWith('clearance-group-')) return;

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
              if (n.id.startsWith('clearance-group-')) {
                return n.style?.background as string || 'rgba(251, 191, 36, 0.2)';
              }
              if (n.style?.background) return n.style.background as string;
              return '#fff';
          }}
          nodeBorderRadius={2}
          className="border border-slate-200 shadow-lg rounded-lg overflow-hidden"
      />

      {/* Legend for clearance colors */}
      <div className="absolute top-4 left-4 bg-white/90 backdrop-blur-sm rounded-lg shadow-md border border-slate-200 p-3 text-xs">
        <div className="font-semibold text-slate-700 mb-2">Clearance Legend</div>
        <div className="space-y-1.5">
          <div className="flex items-center gap-2">
            <div className="w-5 h-5 rounded border-2 border-dashed" style={{ background: 'rgba(251, 191, 36, 0.15)', borderColor: 'rgba(251, 191, 36, 0.5)' }} />
            <span className="text-slate-600">Restricted Group</span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-sm">🔓</span>
            <span className="text-slate-600">Access Allowed (with restrictions)</span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-sm">🔒</span>
            <span className="text-slate-600">Access Denied</span>
          </div>
        </div>
        {clearanceGroups.length > 0 && (
          <div className="mt-2 pt-2 border-t border-slate-200 text-slate-500">
            {clearanceGroups.length} group(s) detected
          </div>
        )}
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
