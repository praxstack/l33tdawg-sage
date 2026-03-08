// CEREBRUM — Your SAGE Brain
import { SSEClient } from './sse.js';
import { fetchStats, fetchGraph, fetchMemories, deleteMemory, fetchHealth, checkAuth, login, importMemories, fetchCleanupSettings, saveCleanupSettings, runCleanup } from './api.js';

const { h, render } = preact;
const { useState, useEffect, useRef, useCallback } = preactHooks;
const html = window.html;

// Domain color palette — vibrant, distinct hues for every domain.
// Known domains get hand-picked colors; unknown domains get a stable
// hash-derived color so each domain is always the same hue.
const DOMAIN_COLORS = {
    crypto: '#06b6d4',
    vuln_intel: '#ef4444',
    challenge_generation: '#8b5cf6',
    solver_feedback: '#10b981',
    calibration: '#f59e0b',
    infrastructure: '#3b82f6',
    general: '#6b7280',
    security: '#f43f5e',
    exploit: '#ec4899',
    'sage-project': '#a78bfa',
    'sage-release': '#2dd4bf',
    'sage-development': '#f472b6',
    'sage-roadmap': '#fbbf24',
    'sage-security': '#fb923c',
    'sage-distribution': '#38bdf8',
    'go-debugging': '#34d399',
    'user-preferences': '#c084fc',
};

const _domainColorCache = {};

function hslToHex(h, s, l) {
    s /= 100; l /= 100;
    const a = s * Math.min(l, 1 - l);
    const f = n => { const k = (n + h / 30) % 12; return l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1)); };
    return '#' + [f(0), f(8), f(4)].map(x => Math.round(x * 255).toString(16).padStart(2, '0')).join('');
}

function getDomainColor(domain) {
    if (!domain) return '#64748b';
    const lower = domain.toLowerCase();

    // Check known domains first
    for (const [key, color] of Object.entries(DOMAIN_COLORS)) {
        if (lower === key || lower.includes(key)) return color;
    }

    // Hash-based color for unknown domains — stable across sessions
    if (_domainColorCache[lower]) return _domainColorCache[lower];
    let hash = 0;
    for (let i = 0; i < lower.length; i++) {
        hash = lower.charCodeAt(i) + ((hash << 5) - hash);
    }
    // Use HSL for vibrant colors, then convert to hex (canvas gradients need hex for alpha suffix)
    const hue = ((hash % 360) + 360) % 360;
    const color = hslToHex(hue, 70, 60);
    _domainColorCache[lower] = color;
    return color;
}

function timeAgo(dateStr) {
    const now = Date.now();
    const then = new Date(dateStr).getTime();
    const diff = Math.floor((now - then) / 1000);
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
}

function confidenceColor(v) {
    if (v >= 0.7) return '#10b981';
    if (v >= 0.4) return '#f59e0b';
    return '#ef4444';
}

// SVG Icons
const icons = {
    brain: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2a7 7 0 0 0-7 7c0 2.38 1.19 4.47 3 5.74V17a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2v-2.26c1.81-1.27 3-3.36 3-5.74a7 7 0 0 0-7-7z"/><line x1="10" y1="22" x2="14" y2="22"/><line x1="9" y1="17" x2="15" y2="17"/></svg>`,
    search: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>`,
    settings: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.32 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>`,
    import: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>`,
    help: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>`,
};

// ============================================================================
// Brain Visualization (Canvas)
// ============================================================================

function BrainView({ sse, onSelectMemory }) {
    const canvasRef = useRef(null);
    const stateRef = useRef({
        nodes: [], edges: [], simulation: null,
        camera: { x: 0, y: 0, zoom: 1 },
        mouse: { x: 0, y: 0, dragging: false, dragStart: null, hoveredNode: null },
        filterDomains: new Set(),
        searchFilter: '',
        animTime: 0,
        pulseNodes: new Map(),
    });
    const [stats, setStats] = useState(null);
    const [domains, setDomains] = useState([]);
    const [filterDomains, setFilterDomains] = useState(new Set());
    const [searchText, setSearchText] = useState('');
    const [tooltip, setTooltip] = useState(null);
    const [sseConnected, setSseConnected] = useState(false);

    // Load graph data
    useEffect(() => {
        loadData();
        const interval = setInterval(loadData, 30000);
        return () => clearInterval(interval);
    }, []);

    async function loadData() {
        try {
            const [graphData, statsData] = await Promise.all([fetchGraph(), fetchStats()]);
            const s = stateRef.current;

            // Preserve positions of existing nodes
            const existingPositions = {};
            for (const n of s.nodes) {
                existingPositions[n.id] = { x: n.x, y: n.y, vx: n.vx, vy: n.vy };
            }

            const nodes = (graphData.nodes || []).map(n => {
                const existing = existingPositions[n.id];
                return {
                    ...n,
                    x: existing ? existing.x : (Math.random() - 0.5) * 600,
                    y: existing ? existing.y : (Math.random() - 0.5) * 400,
                    vx: existing ? existing.vx : 0,
                    vy: existing ? existing.vy : 0,
                    radius: 6 + n.confidence * 14,
                    color: getDomainColor(n.domain),
                };
            });

            s.nodes = nodes;
            s.edges = graphData.edges || [];
            setStats(statsData);

            const domainSet = new Set(nodes.map(n => n.domain).filter(Boolean));
            setDomains([...domainSet].sort());
        } catch (err) {
            // retry on next interval
        }
    }

    // SSE events
    useEffect(() => {
        if (!sse) return;
        const unsub1 = sse.on('connection', (data) => setSseConnected(data.connected));
        const unsub2 = sse.on('remember', (data) => {
            stateRef.current.pulseNodes.set(data.memory_id, { start: performance.now(), type: 'remember' });
            loadData();
        });
        const unsub3 = sse.on('recall', (data) => {
            stateRef.current.pulseNodes.set(data.memory_id, { start: performance.now(), type: 'recall' });
        });
        const unsub4 = sse.on('forget', (data) => {
            stateRef.current.pulseNodes.set(data.memory_id, { start: performance.now(), type: 'forget' });
            setTimeout(loadData, 500);
        });
        return () => { unsub1(); unsub2(); unsub3(); unsub4(); };
    }, [sse]);

    // Canvas rendering loop
    useEffect(() => {
        const canvas = canvasRef.current;
        if (!canvas) return;
        const ctx = canvas.getContext('2d');
        let animFrame;

        function resize() {
            const rect = canvas.parentElement.getBoundingClientRect();
            canvas.width = rect.width * devicePixelRatio;
            canvas.height = rect.height * devicePixelRatio;
            canvas.style.width = rect.width + 'px';
            canvas.style.height = rect.height + 'px';
            ctx.scale(devicePixelRatio, devicePixelRatio);
        }
        resize();
        window.addEventListener('resize', resize);

        function tick() {
            const s = stateRef.current;
            const W = canvas.width / devicePixelRatio;
            const H = canvas.height / devicePixelRatio;
            const now = performance.now();
            s.animTime = now;

            // Update filter state from React
            s.filterDomains = filterDomains;
            s.searchFilter = searchText.toLowerCase();

            // Force simulation
            simulateForces(s, W, H);

            // Clear
            ctx.save();
            ctx.setTransform(1, 0, 0, 1, 0, 0);
            ctx.scale(devicePixelRatio, devicePixelRatio);
            ctx.clearRect(0, 0, W, H);

            // Camera transform
            const cam = s.camera;
            ctx.translate(W / 2 + cam.x, H / 2 + cam.y);
            ctx.scale(cam.zoom, cam.zoom);

            // Determine visible nodes
            const visibleNodes = s.nodes.filter(n => {
                if (s.filterDomains.size > 0 && !s.filterDomains.has(n.domain)) return false;
                return true;
            });
            const visibleIds = new Set(visibleNodes.map(n => n.id));

            // Determine search-matching nodes
            const searchMatch = s.searchFilter
                ? new Set(visibleNodes.filter(n =>
                    (n.content && n.content.toLowerCase().includes(s.searchFilter)) ||
                    (n.domain && n.domain.toLowerCase().includes(s.searchFilter))
                ).map(n => n.id))
                : null;

            // Draw edges
            const nodeMap = {};
            for (const n of s.nodes) nodeMap[n.id] = n;
            for (const e of s.edges) {
                const src = nodeMap[e.source];
                const tgt = nodeMap[e.target];
                if (!src || !tgt) continue;
                if (!visibleIds.has(src.id) || !visibleIds.has(tgt.id)) continue;

                let alpha = 0.08;
                if (searchMatch) {
                    if (searchMatch.has(src.id) || searchMatch.has(tgt.id)) alpha = 0.3;
                    else alpha = 0.02;
                }

                // Pulse animation on recall
                const pulseSrc = s.pulseNodes.get(src.id);
                const pulseTgt = s.pulseNodes.get(tgt.id);
                if (pulseSrc && pulseSrc.type === 'recall' && (now - pulseSrc.start) < 2000) {
                    alpha = 0.8 * (1 - (now - pulseSrc.start) / 2000);
                }
                if (pulseTgt && pulseTgt.type === 'recall' && (now - pulseTgt.start) < 2000) {
                    alpha = Math.max(alpha, 0.8 * (1 - (now - pulseTgt.start) / 2000));
                }

                ctx.beginPath();
                ctx.moveTo(src.x, src.y);
                ctx.lineTo(tgt.x, tgt.y);
                ctx.strokeStyle = src.color;
                ctx.globalAlpha = alpha;
                ctx.lineWidth = 0.5;
                ctx.stroke();
                ctx.globalAlpha = 1;
            }

            // Draw nodes
            for (const n of visibleNodes) {
                let dim = false;
                if (searchMatch && !searchMatch.has(n.id)) dim = true;

                const pulse = s.pulseNodes.get(n.id);
                let extraGlow = 0;
                let fadeOut = 1;

                if (pulse) {
                    const elapsed = now - pulse.start;
                    if (elapsed > 3000) {
                        s.pulseNodes.delete(n.id);
                    } else if (pulse.type === 'remember') {
                        extraGlow = Math.max(0, 1 - elapsed / 1500) * 20;
                    } else if (pulse.type === 'recall') {
                        extraGlow = Math.max(0, 1 - elapsed / 2000) * 15;
                    } else if (pulse.type === 'forget') {
                        fadeOut = Math.max(0, 1 - elapsed / 2000);
                    }
                }

                // Organic drift
                const drift = Math.sin(now / 2000 + n.x * 0.01) * 0.3;

                const drawX = n.x;
                const drawY = n.y + drift;
                const r = n.radius;

                ctx.globalAlpha = dim ? 0.15 * fadeOut : fadeOut;

                // Glow
                const glowSize = r + 8 + extraGlow + Math.sin(now / 1000 + n.x) * 2;
                const glow = ctx.createRadialGradient(drawX, drawY, r * 0.3, drawX, drawY, glowSize);
                glow.addColorStop(0, n.color + '40');
                glow.addColorStop(1, n.color + '00');
                ctx.fillStyle = glow;
                ctx.beginPath();
                ctx.arc(drawX, drawY, glowSize, 0, Math.PI * 2);
                ctx.fill();

                // Node body
                ctx.beginPath();
                ctx.arc(drawX, drawY, r, 0, Math.PI * 2);
                ctx.fillStyle = n.color;
                ctx.globalAlpha = (dim ? 0.2 : 0.85) * fadeOut;
                ctx.fill();

                // Inner highlight
                ctx.beginPath();
                ctx.arc(drawX - r * 0.2, drawY - r * 0.2, r * 0.4, 0, Math.PI * 2);
                ctx.fillStyle = 'rgba(255,255,255,0.25)';
                ctx.globalAlpha = (dim ? 0.1 : 0.5) * fadeOut;
                ctx.fill();

                // Remember ring animation
                if (pulse && pulse.type === 'remember') {
                    const elapsed = now - pulse.start;
                    const ringR = r + (elapsed / 1500) * 40;
                    const ringAlpha = Math.max(0, 1 - elapsed / 1500);
                    ctx.beginPath();
                    ctx.arc(drawX, drawY, ringR, 0, Math.PI * 2);
                    ctx.strokeStyle = n.color;
                    ctx.globalAlpha = ringAlpha * 0.6;
                    ctx.lineWidth = 2;
                    ctx.stroke();
                }

                ctx.globalAlpha = 1;
            }

            // Hover highlight
            if (s.mouse.hoveredNode && visibleIds.has(s.mouse.hoveredNode.id)) {
                const n = s.mouse.hoveredNode;
                ctx.beginPath();
                ctx.arc(n.x, n.y, n.radius + 4, 0, Math.PI * 2);
                ctx.strokeStyle = '#ffffff';
                ctx.globalAlpha = 0.5;
                ctx.lineWidth = 1.5;
                ctx.stroke();
                ctx.globalAlpha = 1;
            }

            ctx.restore();
            animFrame = requestAnimationFrame(tick);
        }

        animFrame = requestAnimationFrame(tick);
        return () => {
            cancelAnimationFrame(animFrame);
            window.removeEventListener('resize', resize);
        };
    }, [filterDomains, searchText]);

    // Mouse interactions
    useEffect(() => {
        const canvas = canvasRef.current;
        if (!canvas) return;

        function screenToWorld(sx, sy) {
            const s = stateRef.current;
            const W = canvas.width / devicePixelRatio;
            const H = canvas.height / devicePixelRatio;
            return {
                x: (sx - W / 2 - s.camera.x) / s.camera.zoom,
                y: (sy - H / 2 - s.camera.y) / s.camera.zoom,
            };
        }

        function findNode(wx, wy) {
            const s = stateRef.current;
            for (let i = s.nodes.length - 1; i >= 0; i--) {
                const n = s.nodes[i];
                const dx = n.x - wx;
                const dy = n.y - wy;
                if (dx * dx + dy * dy < (n.radius + 4) * (n.radius + 4)) return n;
            }
            return null;
        }

        function onMouseMove(e) {
            const rect = canvas.getBoundingClientRect();
            const sx = e.clientX - rect.left;
            const sy = e.clientY - rect.top;
            const s = stateRef.current;
            s.mouse.x = sx;
            s.mouse.y = sy;

            if (s.mouse.dragging) {
                s.camera.x += e.movementX;
                s.camera.y += e.movementY;
                return;
            }

            const w = screenToWorld(sx, sy);
            const node = findNode(w.x, w.y);
            s.mouse.hoveredNode = node;
            canvas.style.cursor = node ? 'pointer' : 'grab';

            if (node) {
                setTooltip({
                    x: e.clientX + 12,
                    y: e.clientY + 12,
                    node,
                });
            } else {
                setTooltip(null);
            }
        }

        function onMouseDown(e) {
            if (e.button === 0) {
                stateRef.current.mouse.dragging = true;
                stateRef.current.mouse.dragStart = { x: e.clientX, y: e.clientY };
                canvas.style.cursor = 'grabbing';
            }
        }

        function onMouseUp(e) {
            const s = stateRef.current;
            const wasDragging = s.mouse.dragging;
            s.mouse.dragging = false;

            if (wasDragging && s.mouse.dragStart) {
                const dx = e.clientX - s.mouse.dragStart.x;
                const dy = e.clientY - s.mouse.dragStart.y;
                if (Math.abs(dx) < 4 && Math.abs(dy) < 4 && s.mouse.hoveredNode) {
                    onSelectMemory(s.mouse.hoveredNode);
                }
            }
            canvas.style.cursor = s.mouse.hoveredNode ? 'pointer' : 'grab';
        }

        function onWheel(e) {
            e.preventDefault();
            const s = stateRef.current;
            const factor = e.deltaY > 0 ? 0.9 : 1.1;
            s.camera.zoom = Math.max(0.1, Math.min(5, s.camera.zoom * factor));
        }

        canvas.addEventListener('mousemove', onMouseMove);
        canvas.addEventListener('mousedown', onMouseDown);
        canvas.addEventListener('mouseup', onMouseUp);
        canvas.addEventListener('mouseleave', () => { setTooltip(null); stateRef.current.mouse.dragging = false; });
        canvas.addEventListener('wheel', onWheel, { passive: false });

        return () => {
            canvas.removeEventListener('mousemove', onMouseMove);
            canvas.removeEventListener('mousedown', onMouseDown);
            canvas.removeEventListener('mouseup', onMouseUp);
            canvas.removeEventListener('mouseleave', () => {});
            canvas.removeEventListener('wheel', onWheel);
        };
    }, [onSelectMemory]);

    function toggleDomain(d) {
        setFilterDomains(prev => {
            const next = new Set(prev);
            if (next.has(d)) next.delete(d);
            else next.add(d);
            return next;
        });
    }

    return html`
        <div class="brain-container">
            <canvas ref=${canvasRef} class="brain-canvas"></canvas>

            <div class="nav-pad">
                <button class="nav-btn nav-up" onClick=${() => { stateRef.current.camera.y += 60; }} title="Pan up">
                    <svg width="12" height="12" viewBox="0 0 12 12"><path d="M6 2L1 8h10z" fill="currentColor"/></svg>
                </button>
                <button class="nav-btn nav-left" onClick=${() => { stateRef.current.camera.x += 60; }} title="Pan left">
                    <svg width="12" height="12" viewBox="0 0 12 12"><path d="M2 6l6-5v10z" fill="currentColor"/></svg>
                </button>
                <button class="nav-btn nav-center" onClick=${() => { stateRef.current.camera.zoom = 1; stateRef.current.camera.x = 0; stateRef.current.camera.y = 0; }} title="Reset view">
                    <svg width="12" height="12" viewBox="0 0 12 12"><circle cx="6" cy="6" r="3" fill="currentColor"/></svg>
                </button>
                <button class="nav-btn nav-right" onClick=${() => { stateRef.current.camera.x -= 60; }} title="Pan right">
                    <svg width="12" height="12" viewBox="0 0 12 12"><path d="M10 6L4 1v10z" fill="currentColor"/></svg>
                </button>
                <button class="nav-btn nav-down" onClick=${() => { stateRef.current.camera.y -= 60; }} title="Pan down">
                    <svg width="12" height="12" viewBox="0 0 12 12"><path d="M6 10L1 4h10z" fill="currentColor"/></svg>
                </button>
                <button class="nav-btn nav-zin" onClick=${() => { stateRef.current.camera.zoom = Math.min(5, stateRef.current.camera.zoom * 1.3); }} title="Zoom in">
                    <svg width="14" height="14" viewBox="0 0 14 14"><line x1="7" y1="3" x2="7" y2="11" stroke="currentColor" stroke-width="2"/><line x1="3" y1="7" x2="11" y2="7" stroke="currentColor" stroke-width="2"/></svg>
                </button>
                <button class="nav-btn nav-zout" onClick=${() => { stateRef.current.camera.zoom = Math.max(0.1, stateRef.current.camera.zoom / 1.3); }} title="Zoom out">
                    <svg width="14" height="14" viewBox="0 0 14 14"><line x1="3" y1="7" x2="11" y2="7" stroke="currentColor" stroke-width="2"/></svg>
                </button>
            </div>

            <div class="domain-bar">
                ${domains.map(d => html`
                    <button class="domain-pill ${filterDomains.has(d) ? 'active' : ''}"
                            style="color: ${getDomainColor(d)}; ${filterDomains.has(d) ? `background: ${getDomainColor(d)}20` : ''}"
                            onClick=${() => toggleDomain(d)}>
                        ${d}
                    </button>
                `)}
            </div>

            <div class="search-overlay">
                <input class="search-input" type="text" placeholder="Filter memories..."
                       value=${searchText} onInput=${e => setSearchText(e.target.value)} />
            </div>

            ${stats && html`
                <div class="stats-panel">
                    <h3>Memory Stats</h3>
                    <div class="stat-row">
                        <span class="stat-label">Total</span>
                        <span class="stat-value">${stats.total_memories || 0}</span>
                    </div>
                    ${stats.by_domain && Object.entries(stats.by_domain).map(([d, c]) => html`
                        <div class="stat-bar-container">
                            <span style="color: ${getDomainColor(d)}; font-size: 11px; min-width: 80px; text-transform: uppercase; letter-spacing: 0.5px;">${d}</span>
                            <div class="stat-bar">
                                <div class="stat-bar-fill" style="width: ${stats.total_memories ? (c / stats.total_memories * 100) : 0}%; background: ${getDomainColor(d)};"></div>
                            </div>
                            <span class="stat-bar-label">${c}</span>
                        </div>
                    `)}
                    ${stats.last_activity && html`
                        <div class="stat-row" style="margin-top: 6px; border-top: 1px solid var(--border); padding-top: 8px;">
                            <span class="stat-label">Last activity</span>
                            <span class="stat-value" style="font-size: 12px;">${timeAgo(stats.last_activity)}</span>
                        </div>
                    `}
                </div>
            `}

            ${tooltip && html`
                <div class="tooltip" style="left: ${tooltip.x}px; top: ${tooltip.y}px;">
                    <div class="tooltip-domain" style="color: ${getDomainColor(tooltip.node.domain)}; background: ${getDomainColor(tooltip.node.domain)}20;">${tooltip.node.domain || 'unknown'}</div>
                    <div class="tooltip-content">${tooltip.node.content ? tooltip.node.content.slice(0, 120) : 'No content'}${tooltip.node.content && tooltip.node.content.length > 120 ? '...' : ''}</div>
                    <div class="tooltip-meta">
                        <span class="tooltip-meta-item">${tooltip.node.memory_type || tooltip.node.memoryType || 'memory'}</span>
                        <span class="tooltip-meta-sep"></span>
                        <span class="tooltip-meta-item" style="color: ${confidenceColor(tooltip.node.confidence)};">${(tooltip.node.confidence * 100).toFixed(0)}%</span>
                        <span class="tooltip-meta-sep"></span>
                        <span class="tooltip-meta-item">${timeAgo(tooltip.node.created_at || tooltip.node.createdAt)}</span>
                    </div>
                </div>
            `}
        </div>
    `;
}

// Simple force simulation
function simulateForces(state, W, H) {
    const nodes = state.nodes;
    const edges = state.edges;
    if (nodes.length === 0) return;

    const dt = 0.3;
    const repulsion = 800;
    const attraction = 0.005;
    const centerGravity = 0.002;
    const damping = 0.92;

    // Build node index
    const nodeIdx = {};
    for (let i = 0; i < nodes.length; i++) nodeIdx[nodes[i].id] = i;

    // Repulsion (Barnes-Hut approximation: only check nearby)
    for (let i = 0; i < nodes.length; i++) {
        for (let j = i + 1; j < nodes.length; j++) {
            const a = nodes[i], b = nodes[j];
            let dx = b.x - a.x;
            let dy = b.y - a.y;
            let dist = Math.sqrt(dx * dx + dy * dy) || 1;
            if (dist > 300) continue; // Skip far nodes
            const force = repulsion / (dist * dist);
            const fx = (dx / dist) * force;
            const fy = (dy / dist) * force;
            a.vx -= fx * dt;
            a.vy -= fy * dt;
            b.vx += fx * dt;
            b.vy += fy * dt;
        }
    }

    // Attraction along edges
    for (const e of edges) {
        const si = nodeIdx[e.source];
        const ti = nodeIdx[e.target];
        if (si === undefined || ti === undefined) continue;
        const a = nodes[si], b = nodes[ti];
        const dx = b.x - a.x;
        const dy = b.y - a.y;
        const fx = dx * attraction;
        const fy = dy * attraction;
        a.vx += fx * dt;
        a.vy += fy * dt;
        b.vx -= fx * dt;
        b.vy -= fy * dt;
    }

    // Center gravity + damping + integration
    for (const n of nodes) {
        n.vx -= n.x * centerGravity;
        n.vy -= n.y * centerGravity;
        n.vx *= damping;
        n.vy *= damping;
        n.x += n.vx * dt;
        n.y += n.vy * dt;
    }
}

// ============================================================================
// Memory Detail Panel
// ============================================================================

function MemoryDetail({ memory, onClose, onDelete }) {
    const [confirming, setConfirming] = useState(false);

    if (!memory) return null;

    async function handleDelete() {
        if (!confirming) { setConfirming(true); return; }
        await deleteMemory(memory.id);
        if (onDelete) onDelete(memory.id);
        onClose();
    }

    const conf = memory.confidence;
    const color = getDomainColor(memory.domain);

    return html`
        <div class="detail-overlay open fade-in">
            <div class="detail-header">
                <h2>Memory Detail</h2>
                <button class="detail-close" onClick=${onClose}>x</button>
            </div>
            <div class="detail-body">
                <div class="detail-section">
                    <label>Content</label>
                    <div class="detail-content">${memory.content || 'No content available'}</div>
                </div>

                <div class="detail-section">
                    <label>Confidence</label>
                    <div class="confidence-bar-container">
                        <div class="confidence-bar">
                            <div class="confidence-bar-fill" style="width: ${conf * 100}%; background: ${confidenceColor(conf)};"></div>
                        </div>
                        <span class="confidence-value" style="color: ${confidenceColor(conf)}">${(conf * 100).toFixed(0)}%</span>
                    </div>
                </div>

                <div class="detail-meta">
                    <div class="detail-meta-item">
                        <label>Domain</label>
                        <span class="domain-badge" style="background: ${color}20; color: ${color};">${memory.domain}</span>
                    </div>
                    <div class="detail-meta-item">
                        <label>Type</label>
                        <span class="value">${memory.memory_type || memory.memoryType || 'unknown'}</span>
                    </div>
                    <div class="detail-meta-item">
                        <label>Status</label>
                        <span class="value">${memory.status}</span>
                    </div>
                    <div class="detail-meta-item">
                        <label>Created</label>
                        <span class="value">${memory.created_at ? timeAgo(memory.created_at) : 'unknown'}</span>
                    </div>
                    <div class="detail-meta-item">
                        <label>Agent</label>
                        <span class="value" style="font-size: 11px; word-break: break-all;">${memory.agent || memory.submitting_agent || 'unknown'}</span>
                    </div>
                    <div class="detail-meta-item">
                        <label>Memory ID</label>
                        <span class="value" style="font-size: 10px; word-break: break-all;">${memory.id || memory.memory_id}</span>
                    </div>
                    ${memory.content_hash && html`
                        <div class="detail-meta-item">
                            <label>Content Hash</label>
                            <span class="value" style="font-size: 10px; word-break: break-all; font-family: var(--font-mono, monospace);">${typeof memory.content_hash === 'string' ? memory.content_hash : btoa(String.fromCharCode(...new Uint8Array(memory.content_hash)))}</span>
                        </div>
                    `}
                    ${memory.committed_at && html`
                        <div class="detail-meta-item">
                            <label>Committed</label>
                            <span class="value">${timeAgo(memory.committed_at)}</span>
                        </div>
                    `}
                    ${memory.provider && html`
                        <div class="detail-meta-item">
                            <label>Provider</label>
                            <span class="value">${memory.provider}</span>
                        </div>
                    `}
                </div>

                <div class="detail-section" style="margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--border);">
                    <button class="btn btn-danger" onClick=${handleDelete}>
                        ${confirming ? 'Confirm Delete' : 'Forget Memory'}
                    </button>
                    ${confirming && html`<span style="font-size: 12px; color: var(--danger); margin-left: 12px;">Click again to confirm</span>`}
                </div>
            </div>
        </div>
    `;
}

// ============================================================================
// Search Page
// ============================================================================

function SearchPage({ onSelectMemory }) {
    const [query, setQuery] = useState('');
    const [results, setResults] = useState([]);
    const [total, setTotal] = useState(0);
    const [loading, setLoading] = useState(false);

    useEffect(() => {
        loadMemories();
    }, []);

    async function loadMemories(search) {
        setLoading(true);
        try {
            const data = await fetchMemories({ limit: 100, sort: 'newest' });
            let memories = data.memories || [];
            if (search) {
                const q = search.toLowerCase();
                memories = memories.filter(m =>
                    (m.content && m.content.toLowerCase().includes(q)) ||
                    (m.domain_tag && m.domain_tag.toLowerCase().includes(q))
                );
            }
            setResults(memories);
            setTotal(data.total || memories.length);
        } catch (err) {
            setResults([]);
        }
        setLoading(false);
    }

    function handleSearch(e) {
        const v = e.target.value;
        setQuery(v);
        loadMemories(v);
    }

    return html`
        <div class="search-page">
            <input class="search-page-input" type="text" placeholder="Search memories..."
                   value=${query} onInput=${handleSearch} />
            <div style="font-size: 12px; color: var(--text-muted); margin-bottom: 12px;">${total} memories</div>
            <div class="memory-list">
                ${results.map(m => html`
                    <div class="memory-card" onClick=${() => onSelectMemory({
                        id: m.memory_id,
                        content: m.content,
                        domain: m.domain_tag,
                        confidence: m.confidence_score,
                        status: m.status,
                        memory_type: m.memory_type,
                        created_at: m.created_at,
                        agent: m.submitting_agent,
                    })}>
                        <div class="memory-card-header">
                            <span class="domain-badge" style="background: ${getDomainColor(m.domain_tag)}20; color: ${getDomainColor(m.domain_tag)};">
                                ${m.domain_tag}
                            </span>
                            <span style="font-size: 12px; font-weight: 600; color: ${confidenceColor(m.confidence_score)};">
                                ${(m.confidence_score * 100).toFixed(0)}%
                            </span>
                        </div>
                        <div class="memory-card-content">${m.content || 'No content'}</div>
                        <div class="memory-card-footer">
                            <span>${m.memory_type} | ${m.status}</span>
                            <span>${m.created_at ? timeAgo(m.created_at) : ''}</span>
                        </div>
                    </div>
                `)}
                ${results.length === 0 && !loading && html`
                    <div style="text-align: center; color: var(--text-muted); padding: 40px;">
                        ${query ? 'No memories match your search.' : 'No memories yet.'}
                    </div>
                `}
            </div>
        </div>
    `;
}

// ============================================================================
// Cleanup Settings Component
// ============================================================================

function CleanupSettings() {
    const [config, setConfig] = useState(null);
    const [saving, setSaving] = useState(false);
    const [lastRun, setLastRun] = useState(null);
    const [lastResult, setLastResult] = useState(null);
    const [cleanupRunning, setCleanupRunning] = useState(false);
    const [cleanupResult, setCleanupResult] = useState(null);
    const [expanded, setExpanded] = useState(false);

    useEffect(() => {
        fetchCleanupSettings().then(data => {
            if (data.config) setConfig(data.config);
            if (data.last_run) setLastRun(data.last_run);
            if (data.last_result) {
                try { setLastResult(JSON.parse(data.last_result)); } catch(e) {}
            }
        }).catch(() => {});
    }, []);

    const updateField = (field, value) => {
        setConfig(prev => ({ ...prev, [field]: value }));
    };

    const handleSave = async () => {
        if (!config || saving) return;
        setSaving(true);
        try {
            const res = await saveCleanupSettings(config);
            if (res.config) setConfig(res.config);
        } catch(e) {}
        setSaving(false);
    };

    const handleDryRun = async () => {
        setCleanupRunning(true);
        setCleanupResult(null);
        try {
            const res = await runCleanup(true);
            setCleanupResult(res);
        } catch(e) {
            setCleanupResult({ error: 'Failed to run preview' });
        }
        setCleanupRunning(false);
    };

    const handleCleanup = async () => {
        if (!confirm('This will permanently deprecate stale memories. Continue?')) return;
        setCleanupRunning(true);
        setCleanupResult(null);
        try {
            const res = await runCleanup(false);
            setCleanupResult(res);
            setLastRun(new Date().toISOString());
            setLastResult(res);
        } catch(e) {
            setCleanupResult({ error: 'Failed to run cleanup' });
        }
        setCleanupRunning(false);
    };

    if (!config) return null;

    return html`
        <div class="settings-section cleanup-section">
            <h3 style="display:flex;align-items:center;justify-content:space-between;cursor:pointer" onClick=${() => setExpanded(!expanded)}>
                <span>
                    <svg width="16" height="16" viewBox="0 0 16 16" style="vertical-align:-2px;margin-right:6px">
                        <path d="M8 1.5a6.5 6.5 0 100 13 6.5 6.5 0 000-13zM8 5v3.5l2.5 1.5" stroke="currentColor" fill="none" stroke-width="1.5" stroke-linecap="round"/>
                    </svg>
                    Memory Auto-Cleanup
                </span>
                <span style="font-size:12px;color:var(--text-muted)">${expanded ? '▲' : '▼'}</span>
            </h3>

            <div class="cleanup-description">
                <p style="color:var(--text-dim);font-size:13px;line-height:1.5;margin:8px 0">
                    Automatically deprecate stale memories whose confidence has decayed below a threshold,
                    or observations that have outlived their usefulness.
                </p>
            </div>

            <!-- Master toggle — always visible -->
            <div class="settings-row" style="padding:12px 0;border-bottom:1px solid var(--border)">
                <div style="flex:1">
                    <span class="label" style="font-weight:600">Enable Auto-Cleanup</span>
                    <div class="setting-help">
                        <span style="color:var(--accent);font-size:11px;font-weight:500">ON:</span>
                        <span style="color:var(--text-dim);font-size:11px"> SAGE periodically removes stale session observations and low-confidence memories. Good for long-running agents that accumulate thousands of memories.</span>
                    </div>
                    <div class="setting-help" style="margin-top:2px">
                        <span style="color:var(--danger);font-size:11px;font-weight:500">OFF:</span>
                        <span style="color:var(--text-dim);font-size:11px"> Nothing is ever auto-removed. You control what stays. Best if you want complete history or have a small memory set.</span>
                    </div>
                </div>
                <label class="toggle-switch">
                    <input type="checkbox" checked=${config.enabled}
                        onChange=${(e) => updateField('enabled', e.target.checked)} />
                    <span class="toggle-slider"></span>
                </label>
            </div>

            ${expanded && html`
                <!-- Observation TTL -->
                <div class="settings-row setting-detail" style="padding:12px 0;border-bottom:1px solid var(--border)">
                    <div style="flex:1">
                        <span class="label">Observation TTL</span>
                        <div class="setting-help">
                            <span style="color:var(--text-dim);font-size:11px">
                                How many days before general observations are auto-deprecated.
                                Observations are things like "user asked about X" or "noticed pattern Y" — useful short-term, less so after a week.
                            </span>
                        </div>
                        <div class="setting-help" style="margin-top:2px">
                            <span style="color:var(--text-muted);font-size:11px;font-style:italic">
                                Example: Set to 7 days if your agent logs dozens of observations per session. Set to 30+ if observations are rare and valuable.
                            </span>
                        </div>
                    </div>
                    <div style="display:flex;align-items:center;gap:8px">
                        <input type="range" min="1" max="90" value=${config.observation_ttl_days}
                            onInput=${(e) => updateField('observation_ttl_days', parseInt(e.target.value))}
                            style="width:120px" />
                        <span class="value" style="min-width:50px;text-align:right">${config.observation_ttl_days}d</span>
                    </div>
                </div>

                <!-- Session TTL -->
                <div class="settings-row setting-detail" style="padding:12px 0;border-bottom:1px solid var(--border)">
                    <div style="flex:1">
                        <span class="label">Session Context TTL</span>
                        <div class="setting-help">
                            <span style="color:var(--text-dim);font-size:11px">
                                How many days before session-context observations expire. These are ephemeral notes like
                                "user said good morning" or "started new session" — they clutter your memory fast.
                            </span>
                        </div>
                        <div class="setting-help" style="margin-top:2px">
                            <span style="color:var(--text-muted);font-size:11px;font-style:italic">
                                Example: Set to 1-2 days for aggressive cleanup. Set to 7 if you want a week of session history.
                            </span>
                        </div>
                    </div>
                    <div style="display:flex;align-items:center;gap:8px">
                        <input type="range" min="1" max="30" value=${config.session_ttl_days}
                            onInput=${(e) => updateField('session_ttl_days', parseInt(e.target.value))}
                            style="width:120px" />
                        <span class="value" style="min-width:50px;text-align:right">${config.session_ttl_days}d</span>
                    </div>
                </div>

                <!-- Stale Threshold -->
                <div class="settings-row setting-detail" style="padding:12px 0;border-bottom:1px solid var(--border)">
                    <div style="flex:1">
                        <span class="label">Stale Confidence Threshold</span>
                        <div class="setting-help">
                            <span style="color:var(--text-dim);font-size:11px">
                                Memories whose computed confidence drops below this value get auto-deprecated.
                                Confidence decays naturally over time — facts decay slowly (~139 day half-life),
                                while observations decay faster.
                            </span>
                        </div>
                        <div class="setting-help" style="margin-top:2px">
                            <span style="color:var(--text-muted);font-size:11px;font-style:italic">
                                Example: 0.10 is conservative (only removes very stale memories). 0.25 is aggressive (removes anything that's lost 75% confidence).
                            </span>
                        </div>
                    </div>
                    <div style="display:flex;align-items:center;gap:8px">
                        <input type="range" min="1" max="50" value=${Math.round(config.stale_threshold * 100)}
                            onInput=${(e) => updateField('stale_threshold', parseInt(e.target.value) / 100)}
                            style="width:120px" />
                        <span class="value" style="min-width:50px;text-align:right">${(config.stale_threshold * 100).toFixed(0)}%</span>
                    </div>
                </div>

                <!-- Cleanup Interval -->
                <div class="settings-row setting-detail" style="padding:12px 0;border-bottom:1px solid var(--border)">
                    <div style="flex:1">
                        <span class="label">Cleanup Interval</span>
                        <div class="setting-help">
                            <span style="color:var(--text-dim);font-size:11px">
                                How often the background cleanup runs (in hours). Lower = more frequent checks.
                            </span>
                        </div>
                        <div class="setting-help" style="margin-top:2px">
                            <span style="color:var(--text-muted);font-size:11px;font-style:italic">
                                Example: 24h (once a day) is fine for most users. Set to 1h if you're generating memories rapidly.
                            </span>
                        </div>
                    </div>
                    <div style="display:flex;align-items:center;gap:8px">
                        <input type="range" min="1" max="168" value=${config.cleanup_interval_hours}
                            onInput=${(e) => updateField('cleanup_interval_hours', parseInt(e.target.value))}
                            style="width:120px" />
                        <span class="value" style="min-width:50px;text-align:right">${config.cleanup_interval_hours}h</span>
                    </div>
                </div>

                <!-- Save button -->
                <div style="display:flex;gap:8px;margin-top:16px;flex-wrap:wrap">
                    <button class="btn btn-primary" onClick=${handleSave} disabled=${saving}>
                        ${saving ? 'Saving...' : 'Save Settings'}
                    </button>
                    <button class="btn" onClick=${handleDryRun} disabled=${cleanupRunning}>
                        ${cleanupRunning ? 'Running...' : 'Preview Cleanup'}
                    </button>
                    <button class="btn btn-danger" onClick=${handleCleanup} disabled=${cleanupRunning}>
                        Run Cleanup Now
                    </button>
                </div>

                <!-- Cleanup result -->
                ${cleanupResult && html`
                    <div class="cleanup-result" style="margin-top:12px;padding:12px;background:var(--bg-surface);border:1px solid var(--border);border-radius:var(--radius)">
                        ${cleanupResult.error ? html`
                            <span style="color:var(--danger)">${cleanupResult.error}</span>
                        ` : html`
                            <div style="font-size:13px;color:var(--text-dim)">
                                <strong style="color:var(--text)">${cleanupResult.dry_run ? 'Preview' : 'Cleanup Complete'}</strong>
                                <span style="margin-left:8px">
                                    Checked: ${cleanupResult.checked} ·
                                    ${cleanupResult.dry_run ? 'Would deprecate' : 'Deprecated'}: <strong style="color:${cleanupResult.deprecated > 0 ? 'var(--warning)' : 'var(--accent)'}">${cleanupResult.deprecated}</strong>
                                </span>
                            </div>
                            ${cleanupResult.deprecated_ids && cleanupResult.deprecated_ids.length > 0 && html`
                                <div style="margin-top:8px;font-size:11px;color:var(--text-muted);max-height:100px;overflow-y:auto">
                                    ${cleanupResult.deprecated_ids.map(id => html`<div style="font-family:monospace">${id.substring(0, 8)}...</div>`)}
                                </div>
                            `}
                        `}
                    </div>
                `}

                <!-- Last run info -->
                ${lastRun && html`
                    <div style="margin-top:8px;font-size:11px;color:var(--text-muted)">
                        Last cleanup: ${new Date(lastRun).toLocaleString()}
                        ${lastResult && lastResult.deprecated != null ? html` · Deprecated: ${lastResult.deprecated}` : ''}
                    </div>
                `}
            `}
        </div>
    `;
}

// ============================================================================
// Settings Page
// ============================================================================

function SettingsPage() {
    const [stats, setStats] = useState(null);
    const [health, setHealth] = useState(null);

    // Fetch health with live polling every 3s
    useEffect(() => {
        const poll = () => {
            fetchHealth().then(h => {
                setHealth(h);
            }).catch(() => {});
            fetchStats().then(setStats).catch(() => {});
        };
        poll();
        const iv = setInterval(poll, 3000);
        return () => clearInterval(iv);
    }, []);

    // Countdown ticker — force re-render every 100ms for smooth display
    const [, setTick] = useState(0);
    useEffect(() => {
        const iv = setInterval(() => setTick(t => t + 1), 100);
        return () => clearInterval(iv);
    }, []);

    const ver = health?.version || 'dev';
    const encrypted = health?.encrypted || false;
    const chain = health?.chain || null;
    const ollama = health?.ollama || 'unknown';
    const uptime = health?.uptime || '--';

    // Format countdown — compute from block_time relative to now
    const getCountdown = () => {
        if (!chain?.block_time) return null;
        const lastBlock = new Date(chain.block_time).getTime();
        const blockInterval = 5000;
        const elapsed = Date.now() - lastBlock;
        const remaining = blockInterval - (elapsed % blockInterval);
        return remaining;
    };
    const liveCountdown = getCountdown();
    const countdownDisplay = liveCountdown !== null ? (liveCountdown / 1000).toFixed(1) + 's' : '--';
    const countdownPct = liveCountdown !== null ? Math.min(100, (liveCountdown / 5000) * 100) : 0;

    // Status indicator dot
    const statusDot = (active) => html`
        <span class="status-dot ${active ? 'active' : 'inactive'}"></span>
    `;

    // Helper: format nanosecond duration to human-readable
    const formatDuration = (nsStr) => {
        const ns = parseInt(nsStr);
        if (isNaN(ns)) return '--';
        const hours = Math.floor(ns / 3.6e12);
        const mins = Math.floor((ns % 3.6e12) / 6e10);
        if (hours > 24) return Math.floor(hours / 24) + 'd ' + (hours % 24) + 'h';
        if (hours > 0) return hours + 'h ' + mins + 'm';
        return mins + 'm';
    };

    const formatBytes = (bytesStr) => {
        const b = parseInt(bytesStr);
        if (isNaN(b)) return '0 B';
        if (b < 1024) return b + ' B';
        if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
        return (b / 1048576).toFixed(1) + ' MB';
    };

    const peers = chain?.peer_list || [];

    return html`
        <div class="settings-page">
            <!-- Chain Health — full width hero -->
            <div class="settings-section chain-health-section">
                <h3>
                    <svg width="16" height="16" viewBox="0 0 16 16" style="vertical-align:-2px;margin-right:6px">
                        <path d="M4 4h3v3H4zM9 4h3v3H9zM4 9h3v3H4zM9 9h3v3H9z" fill="currentColor" opacity="0.8"/>
                        <path d="M2 2h12v12H2z" stroke="currentColor" fill="none" stroke-width="1.5" rx="2"/>
                    </svg>
                    Chain Health
                </h3>

                ${chain ? html`
                    <div class="chain-stats-grid">
                        <div class="chain-stat-card" title="Total number of blocks committed to the chain. Each block contains validated memory operations.">
                            <div class="chain-stat-value block-height">${Number(chain.block_height || 0).toLocaleString()}</div>
                            <div class="chain-stat-label">Block Height</div>
                        </div>
                        <div class="chain-stat-card" title="Countdown to the next block being produced (~5s intervals). Memories are committed to the chain in blocks.">
                            <div class="chain-stat-value countdown-value">${countdownDisplay}</div>
                            <div class="chain-stat-label">Next Block</div>
                            <div class="countdown-bar">
                                <div class="countdown-fill" style="width: ${countdownPct}%"></div>
                            </div>
                        </div>
                        <div class="chain-stat-card" title="Number of other SAGE nodes connected in quorum mode. 0 = running solo (Personal mode).">
                            <div class="chain-stat-value">${chain.peers || '0'}</div>
                            <div class="chain-stat-label">Peers</div>
                        </div>
                        <div class="chain-stat-card" title="This validator's voting power in the BFT consensus. Higher = more influence on which memories get committed.">
                            <div class="chain-stat-value">${chain.voting_power || '0'}</div>
                            <div class="chain-stat-label">Voting Power</div>
                        </div>
                    </div>

                    <div class="chain-details">
                        <div class="settings-row" title="Unique identifier for this blockchain network. Each SAGE deployment has its own chain.">
                            <span class="label">Chain ID</span>
                            <span class="value chain-id-value">${chain.chain_id || '--'}</span>
                        </div>
                        <div class="settings-row" title="The display name of this SAGE node. Set during initialization.">
                            <span class="label">Node</span>
                            <span class="value">${chain.moniker || '--'}</span>
                        </div>
                        <div class="settings-row" title="Whether this node is catching up with the latest blocks. 'In sync' means it's up to date.">
                            <span class="label">Syncing</span>
                            <span class="value" style="color: ${chain.catching_up ? '#ef4444' : '#10b981'}">
                                ${chain.catching_up ? 'Catching up...' : 'In sync'}
                            </span>
                        </div>
                        <div class="settings-row" title="Timestamp of the most recently committed block.">
                            <span class="label">Last Block</span>
                            <span class="value">${chain.block_time ? new Date(chain.block_time).toLocaleTimeString() : '--'}</span>
                        </div>
                    </div>
                ` : html`
                    <div class="chain-offline">
                        ${statusDot(false)}
                        <span>Chain unavailable — CometBFT not running</span>
                    </div>
                `}
            </div>

            <!-- Two-column grid for the rest -->
            <div class="settings-grid">
                <!-- Left column: System Status -->
                <div class="settings-section">
                    <h3>System Status</h3>
                    <div class="settings-row" title="SAGE memory engine status. If you can see this, it's running.">
                        <span class="label">${statusDot(true)} SAGE</span>
                        <span class="value" style="color:#10b981">Running</span>
                    </div>
                    <div class="settings-row" title="Ollama provides local AI embeddings for semantic search. Optional — SAGE falls back to hash-based embeddings if offline.">
                        <span class="label">${statusDot(ollama === 'running')} Ollama</span>
                        <span class="value" style="color: ${ollama === 'running' ? '#10b981' : '#6b7280'}">
                            ${ollama === 'running' ? 'Connected' : 'Offline'}
                        </span>
                    </div>
                    <div class="settings-row" title="When enabled, all memories are encrypted at rest with AES-256-GCM. Requires a vault passphrase to unlock.">
                        <span class="label">${statusDot(encrypted)} Encryption</span>
                        <span class="value" style="color: ${encrypted ? '#10b981' : '#6b7280'}">
                            ${encrypted ? 'AES-256-GCM' : 'Off'}
                        </span>
                    </div>
                    <div class="settings-row" title="Current SAGE version.">
                        <span class="label">Version</span>
                        <span class="value">${ver}</span>
                    </div>
                    <div class="settings-row" title="How long SAGE has been running since last restart.">
                        <span class="label">Uptime</span>
                        <span class="value">${uptime}</span>
                    </div>
                    <div class="settings-row" title="The REST API endpoint that your AI agents connect to for memory operations.">
                        <span class="label">API Endpoint</span>
                        <span class="value">${window.location.origin}</span>
                    </div>
                </div>

                <!-- Right column: Memory Statistics -->
                ${stats ? html`
                    <div class="settings-section">
                        <h3>Memory Statistics</h3>
                        <div class="settings-row" title="Total number of memories across all statuses and domains.">
                            <span class="label">Total Memories</span>
                            <span class="value">${stats.total_memories || 0}</span>
                        </div>
                        ${stats.by_status && Object.entries(stats.by_status).map(([s, c]) => html`
                            <div class="settings-row">
                                <span class="label">${s}</span>
                                <span class="value">${c}</span>
                            </div>
                        `)}
                        ${stats.db_size_bytes != null && html`
                            <div class="settings-row">
                                <span class="label">DB Size</span>
                                <span class="value">${(stats.db_size_bytes / 1024 / 1024).toFixed(1)} MB</span>
                            </div>
                        `}
                    </div>
                ` : html`<div></div>`}

                <!-- Peers Section -->
                <div class="settings-section">
                    <h3>
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="vertical-align:-2px;margin-right:6px">
                            <path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/>
                            <circle cx="9" cy="7" r="4"/>
                            <path d="M23 21v-2a4 4 0 0 0-3-3.87"/>
                            <path d="M16 3.13a4 4 0 0 1 0 7.75"/>
                        </svg>
                        Connected Peers
                    </h3>
                    ${peers.length > 0 ? peers.map(p => html`
                        <div class="peer-card">
                            <div class="peer-header">
                                <span class="status-dot active"></span>
                                <span class="peer-moniker">${p.moniker || 'unknown'}</span>
                                <span class="peer-badge">${p.outbound ? 'outbound' : 'inbound'}</span>
                            </div>
                            <div class="peer-meta">
                                <span class="peer-meta-label">IP</span>
                                <span class="peer-meta-value">${p.remote_ip}</span>
                                <span class="peer-meta-label">Connected</span>
                                <span class="peer-meta-value">${formatDuration(p.duration)}</span>
                                <span class="peer-meta-label">Sent</span>
                                <span class="peer-meta-value">${formatBytes(p.bytes_sent)}</span>
                                <span class="peer-meta-label">Received</span>
                                <span class="peer-meta-value">${formatBytes(p.bytes_recv)}</span>
                                <span class="peer-meta-label">Node ID</span>
                                <span class="peer-meta-value">${p.id}...</span>
                            </div>
                        </div>
                    `) : html`
                        <div class="peer-empty">
                            No peers connected — running in Personal mode.
                            <div style="margin-top:8px;font-size:11px;color:var(--text-muted)">
                                Connect other SAGE nodes via quorum mode to see peers here.
                            </div>
                        </div>
                    `}
                </div>

                <!-- Export + About -->
                <div class="settings-section">
                    <h3>Export & About</h3>
                    <div class="settings-row">
                        <span class="label">Export all memories</span>
                        <button class="btn" onClick=${() => {
                            fetchMemories({ limit: 10000 }).then(data => {
                                const blob = new Blob([JSON.stringify(data.memories, null, 2)], { type: 'application/json' });
                                const url = URL.createObjectURL(blob);
                                const a = document.createElement('a');
                                a.href = url;
                                a.download = 'sage-memories-export.json';
                                a.click();
                                URL.revokeObjectURL(url);
                            });
                        }}>Download JSON</button>
                    </div>
                    <div style="border-top:1px solid var(--border);margin-top:8px;padding-top:8px">
                        <div class="settings-row">
                            <span class="label">Full Name</span>
                            <span class="value">(Sovereign) Agent Governed Experience</span>
                        </div>
                        <div class="settings-row">
                            <span class="label">Author</span>
                            <span class="value">Dhillon Andrew Kannabhiran</span>
                        </div>
                        <div class="settings-row">
                            <span class="label">License</span>
                            <span class="value">Apache 2.0</span>
                        </div>
                        <div class="settings-row">
                            <span class="label">GitHub</span>
                            <span class="value"><a href="https://github.com/l33tdawg/sage" target="_blank" style="color:var(--accent)">l33tdawg/sage</a></span>
                        </div>
                        <div class="settings-row">
                            <span class="label">Website</span>
                            <span class="value"><a href="https://l33tdawg.github.io/sage/" target="_blank" style="color:var(--accent)">l33tdawg.github.io/sage</a></span>
                        </div>
                        <div class="settings-row">
                            <span class="label">Architecture</span>
                            <span class="value">CometBFT v0.38 + SQLite + Ed25519</span>
                        </div>
                        <div class="settings-row">
                            <span class="label">Connect Guide</span>
                            <span class="value"><a href="https://l33tdawg.github.io/sage/connect.html" target="_blank" style="color:var(--accent)">How to connect your AI</a></span>
                        </div>
                    </div>
                </div>

                <!-- Auto-Cleanup — full width -->
                <div class="settings-section full-width">
                    ${html`<${CleanupSettings} />`}
                </div>
            </div>
        </div>
    `;
}

// ============================================================================
// Import Page
// ============================================================================

function ImportPage() {
    const [selectedFile, setSelectedFile] = useState(null);
    const [dragging, setDragging] = useState(false);
    const [importing, setImporting] = useState(false);
    const [result, setResult] = useState(null);
    const [error, setError] = useState(null);
    const fileInputRef = useRef(null);

    function handleDragOver(e) {
        e.preventDefault();
        e.stopPropagation();
        setDragging(true);
    }

    function handleDragLeave(e) {
        e.preventDefault();
        e.stopPropagation();
        setDragging(false);
    }

    function handleDrop(e) {
        e.preventDefault();
        e.stopPropagation();
        setDragging(false);
        const file = e.dataTransfer.files[0];
        if (file && (file.name.endsWith('.json') || file.name.endsWith('.zip'))) {
            setSelectedFile(file);
            setResult(null);
            setError(null);
        } else {
            setError('Please drop a .json or .zip file.');
        }
    }

    function handleFileSelect(e) {
        const file = e.target.files[0];
        if (file) {
            setSelectedFile(file);
            setResult(null);
            setError(null);
        }
    }

    async function handleImport() {
        if (!selectedFile || importing) return;
        setImporting(true);
        setError(null);
        setResult(null);
        try {
            const res = await importMemories(selectedFile);
            if (res.error) {
                setError(res.error);
            } else {
                setResult(res);
            }
        } catch (err) {
            setError(err.message || 'Import failed. Please try again.');
        } finally {
            setImporting(false);
        }
    }

    function formatFileSize(bytes) {
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    }

    return html`
        <div class="import-page">
            <div class="import-header">
                <h2>Import Memories</h2>
                <p class="import-subtitle">Bring your AI conversations into SAGE</p>
            </div>

            <div class="provider-cards">
                <div class="provider-card">
                    <div class="provider-icon">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="32" height="32">
                            <circle cx="12" cy="12" r="10"/>
                            <path d="M8 12l2-4h4l2 4-2 4h-4l-2-4z"/>
                        </svg>
                    </div>
                    <h3>ChatGPT</h3>
                    <p>Export from <strong>Settings</strong> > <strong>Data Controls</strong> > <strong>Export Data</strong>. Upload the ZIP.</p>
                    <span class="provider-file-type">.zip</span>
                </div>
                <div class="provider-card">
                    <div class="provider-icon">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="32" height="32">
                            <path d="M12 2L2 7l10 5 10-5-10-5z"/>
                            <path d="M2 17l10 5 10-5"/>
                            <path d="M2 12l10 5 10-5"/>
                        </svg>
                    </div>
                    <h3>Claude.ai</h3>
                    <p>Export from <strong>Settings</strong> > <strong>Privacy</strong> > <strong>Export Data</strong>. Upload the JSON.</p>
                    <span class="provider-file-type">.json</span>
                </div>
                <div class="provider-card">
                    <div class="provider-icon">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="32" height="32">
                            <path d="M12 2l3.09 6.26L22 9.27l-5 4.87 1.18 6.88L12 17.77l-6.18 3.25L7 14.14 2 9.27l6.91-1.01L12 2z"/>
                        </svg>
                    </div>
                    <h3>Gemini</h3>
                    <p>Export from <strong>Google Takeout</strong> > <strong>My Activity</strong> > <strong>Gemini Apps</strong>. Upload the JSON.</p>
                    <span class="provider-file-type">.json</span>
                </div>
            </div>

            <div class="drop-zone ${dragging ? 'drop-zone-active' : ''} ${selectedFile ? 'drop-zone-has-file' : ''}"
                 onDragOver=${handleDragOver}
                 onDragLeave=${handleDragLeave}
                 onDrop=${handleDrop}
                 onClick=${() => fileInputRef.current && fileInputRef.current.click()}>
                <input type="file" ref=${fileInputRef} accept=".json,.zip"
                       style="display:none" onChange=${handleFileSelect} />
                ${selectedFile ? html`
                    <div class="drop-zone-file">
                        <svg viewBox="0 0 24 24" fill="none" stroke="var(--accent)" stroke-width="2" width="28" height="28">
                            <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
                            <polyline points="14 2 14 8 20 8"/>
                        </svg>
                        <div>
                            <div class="drop-zone-filename">${selectedFile.name}</div>
                            <div class="drop-zone-filesize">${formatFileSize(selectedFile.size)}</div>
                        </div>
                    </div>
                ` : html`
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="40" height="40" style="opacity:0.5">
                        <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
                        <polyline points="17 8 12 3 7 8"/>
                        <line x1="12" y1="3" x2="12" y2="15"/>
                    </svg>
                    <p class="drop-zone-text">Drop your export file here or click to browse</p>
                    <span class="drop-zone-hint">Accepts .zip and .json files</span>
                `}
            </div>

            <div class="import-actions">
                <button class="btn import-btn ${importing ? 'importing' : ''}"
                        disabled=${!selectedFile || importing}
                        onClick=${handleImport}>
                    ${importing ? html`
                        <span class="import-spinner"></span> Importing...
                    ` : 'Import Memories'}
                </button>
            </div>

            ${error && html`
                <div class="import-error">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="18" height="18">
                        <circle cx="12" cy="12" r="10"/>
                        <line x1="15" y1="9" x2="9" y2="15"/>
                        <line x1="9" y1="9" x2="15" y2="15"/>
                    </svg>
                    <span>${error}</span>
                </div>
            `}

            ${result && html`
                <div class="import-results fade-in">
                    <div class="import-results-header">
                        <svg viewBox="0 0 24 24" fill="none" stroke="var(--accent)" stroke-width="2" width="24" height="24">
                            <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
                            <polyline points="22 4 12 14.01 9 11.01"/>
                        </svg>
                        <h3>Import Complete</h3>
                    </div>
                    <div class="import-results-stats">
                        ${result.imported != null && html`
                            <div class="import-stat">
                                <span class="import-stat-value">${result.imported}</span>
                                <span class="import-stat-label">memories imported${result.provider ? ` from ${result.provider}` : ''}</span>
                            </div>
                        `}
                        ${result.skipped != null && result.skipped > 0 && html`
                            <div class="import-stat">
                                <span class="import-stat-value import-stat-dim">${result.skipped}</span>
                                <span class="import-stat-label">skipped (duplicates or empty)</span>
                            </div>
                        `}
                        ${result.errors && result.errors.length > 0 && html`
                            <div class="import-stat">
                                <span class="import-stat-value import-stat-warn">${result.errors.length}</span>
                                <span class="import-stat-label">errors</span>
                            </div>
                            <div class="import-error-list">
                                ${result.errors.slice(0, 5).map(e => html`<div class="import-error-item">${e}</div>`)}
                                ${result.errors.length > 5 && html`<div class="import-error-item">...and ${result.errors.length - 5} more</div>`}
                            </div>
                        `}
                    </div>
                    <button class="btn import-view-btn" onClick=${() => { window.location.hash = '/'; }}>
                        View in Brain
                    </button>
                </div>
            `}
        </div>
    `;
}

// ============================================================================
// Timeline Bar
// ============================================================================

function TimelineBar() {
    const [buckets, setBuckets] = useState([]);

    useEffect(() => {
        import('./api.js').then(({ fetchTimeline }) => {
            fetchTimeline({ bucket: 'hour' }).then(data => {
                setBuckets(data.buckets || []);
            }).catch(() => {});
        });
    }, []);

    const maxCount = Math.max(1, ...buckets.map(b => b.count));

    function formatPeriod(period) {
        try {
            const d = new Date(period);
            return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        } catch (e) {
            return period;
        }
    }

    return html`
        <div class="timeline-bar">
            <span class="timeline-label">24h</span>
            <div class="timeline-track">
                ${buckets.map((b, i) => {
                    const pct = (b.count / maxCount) * 100;
                    return html`
                        <div class="timeline-bucket-bar"
                             style="left: ${(i / Math.max(1, buckets.length)) * 100}%;
                                    width: ${100 / Math.max(1, buckets.length)}%;
                                    height: ${Math.max(pct, 4)}%;">
                            <div class="timeline-tooltip">
                                <span class="timeline-tooltip-count">${b.count}</span> memor${b.count === 1 ? 'y' : 'ies'}
                                <br/>
                                <span class="timeline-tooltip-time">${formatPeriod(b.period)}</span>
                            </div>
                        </div>
                    `;
                })}
            </div>
            <span class="timeline-label" style="text-align: right;">Now</span>
        </div>
    `;
}

// ============================================================================
// Health Status Bar
// ============================================================================

function HealthBar() {
    const [health, setHealth] = useState(null);

    useEffect(() => {
        loadHealth();
        const interval = setInterval(loadHealth, 15000);
        return () => clearInterval(interval);
    }, []);

    async function loadHealth() {
        try {
            const data = await fetchHealth();
            setHealth(data);
        } catch (e) {
            setHealth(null);
        }
    }

    if (!health) return null;

    const ollamaOk = health.ollama === 'running';
    const totalMem = health.memories?.total_memories || 0;
    const domains = health.memories?.by_domain ? Object.keys(health.memories.by_domain).length : 0;

    return html`
        <div class="health-bar">
            <div class="health-item">
                <div class="health-dot ${ollamaOk ? 'ok' : 'err'}"></div>
                <span>Ollama ${ollamaOk ? 'connected' : 'offline'}</span>
            </div>
            <div class="health-sep"></div>
            <div class="health-item">
                <span class="health-num">${totalMem}</span> memories
            </div>
            <div class="health-sep"></div>
            <div class="health-item">
                <span class="health-num">${domains}</span> domains
            </div>
            <div class="health-sep"></div>
            <div class="health-item">
                <span style="color: var(--text-muted)">uptime</span> ${health.uptime ? health.uptime.split('.')[0] : '—'}
            </div>
        </div>
    `;
}

// ============================================================================
// Help Overlay
// ============================================================================

function HelpOverlay({ onClose }) {
    const [dontShow, setDontShow] = useState(false);

    function handleDismiss() {
        if (dontShow) {
            try { localStorage.setItem('sage-help-dismissed', '1'); } catch (e) {}
        }
        onClose();
    }

    const cards = [
        {
            title: 'Cerebrum View',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2a7 7 0 0 0-7 7c0 2.38 1.19 4.47 3 5.74V17a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2v-2.26c1.81-1.27 3-3.36 3-5.74a7 7 0 0 0-7-7z"/></svg>`,
            body: 'Each bubble is a memory. Size = confidence level. Color = knowledge domain. Click a bubble to see details.',
        },
        {
            title: 'Filtering',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3"/></svg>`,
            body: 'Click domain pills at the top to filter. Use the search box to find specific memories.',
        },
        {
            title: 'Navigation',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="3 11 22 2 13 21 11 13 3 11"/></svg>`,
            body: 'Scroll to zoom, drag to pan, or use the navigation pad. Click a bubble to see its content.',
        },
        {
            title: 'Timeline',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="20" x2="12" y2="10"/><line x1="18" y1="20" x2="18" y2="4"/><line x1="6" y1="20" x2="6" y2="16"/></svg>`,
            body: 'The bar at the bottom shows memory activity over the last 24 hours. Hover segments to see counts.',
        },
        {
            title: 'Domains',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>`,
            body: "Domains are categories like 'sage-architecture' or 'user-prefs'. They're created automatically based on what you discuss.",
        },
        {
            title: 'Deleting',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6"/><path d="M14 11v6"/></svg>`,
            body: "Click a memory bubble, then click Delete in the detail panel. Deleted memories are marked as deprecated -- they're hidden from recall but not permanently erased.",
        },
        {
            title: 'Import',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>`,
            body: 'Go to Import (upload icon in sidebar) to bring in conversations from ChatGPT, Claude, or Gemini.',
        },
        {
            title: 'Search',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>`,
            body: 'The search page (magnifying glass icon) lets you do full-text search across all memories.',
        },
        {
            title: 'Auto-Cleanup',
            icon: html`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2a10 10 0 100 20 10 10 0 000-20zM12 6v6l4 2"/></svg>`,
            body: 'Go to Settings to enable auto-cleanup. It removes stale observations and low-confidence memories over time, keeping your brain lean.',
        },
    ];

    return html`
        <div class="help-overlay" onClick=${(e) => { if (e.target === e.currentTarget) handleDismiss(); }}>
            <div class="help-modal">
                <div class="help-modal-header">
                    <h2>CEREBRUM Guide</h2>
                    <button class="detail-close" onClick=${handleDismiss}>x</button>
                </div>
                <div class="help-grid">
                    ${cards.map(c => html`
                        <div class="help-card">
                            <div class="help-card-title">${c.icon} ${c.title}</div>
                            <div class="help-card-body">${c.body}</div>
                        </div>
                    `)}
                </div>
                <div class="help-footer">
                    <label class="help-dismiss-check">
                        <input type="checkbox" checked=${dontShow}
                               onChange=${(e) => setDontShow(e.target.checked)} />
                        Don't show again
                    </label>
                    <button class="btn" style="background: var(--primary); color: #fff; border-color: var(--primary); font-weight: 600;"
                            onClick=${handleDismiss}>Got it</button>
                </div>
            </div>
        </div>
    `;
}

// ============================================================================
// Login Screen (shown when vault encryption requires auth)
// ============================================================================

function LoginScreen({ onSuccess }) {
    const [passphrase, setPassphrase] = useState('');
    const [error, setError] = useState('');
    const [loading, setLoading] = useState(false);

    async function handleSubmit(e) {
        e.preventDefault();
        if (!passphrase) return;
        setLoading(true);
        setError('');
        try {
            const res = await login(passphrase);
            if (res.ok) {
                onSuccess();
            } else {
                setError(res.error || 'Wrong passphrase');
            }
        } catch (err) {
            setError('Connection failed');
        }
        setLoading(false);
    }

    return html`
        <div class="login-screen">
            <div class="login-card">
                <div class="login-icon">
                    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="var(--accent, #a78bfa)" stroke-width="1.5">
                        <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
                        <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
                        <circle cx="12" cy="16" r="1"/>
                    </svg>
                </div>
                <h2 class="login-title">SAGE is Encrypted</h2>
                <p class="login-subtitle">Enter your vault passphrase to unlock CEREBRUM.</p>
                <form onSubmit=${handleSubmit}>
                    <input
                        type="password"
                        class="login-input"
                        placeholder="Vault passphrase"
                        value=${passphrase}
                        onInput=${e => setPassphrase(e.target.value)}
                        autofocus
                    />
                    ${error && html`<div class="login-error">${error}</div>`}
                    <button type="submit" class="login-btn" disabled=${loading || !passphrase}>
                        ${loading ? 'Unlocking...' : 'Unlock'}
                    </button>
                </form>
            </div>
        </div>
    `;
}

// ============================================================================
// Root App
// ============================================================================

function App() {
    const [authState, setAuthState] = useState('loading'); // loading | login | ready
    const [page, setPage] = useState('brain');
    const [selectedMemory, setSelectedMemory] = useState(null);
    const [sseConnected, setSseConnected] = useState(false);
    const [showHelp, setShowHelp] = useState(false);
    const sseRef = useRef(null);

    // Check auth on mount
    useEffect(() => {
        checkAuth().then(res => {
            if (!res.auth_required || res.authenticated) {
                setAuthState('ready');
            } else {
                setAuthState('login');
            }
        }).catch(() => setAuthState('ready')); // if auth check fails, assume no auth
    }, []);

    useEffect(() => {
        if (authState !== 'ready') return;

        const sse = new SSEClient();
        sse.connect();
        sseRef.current = sse;
        sse.on('connection', (data) => setSseConnected(data.connected));

        // Hash routing
        function onHash() {
            const hash = window.location.hash.slice(1) || '/';
            if (hash === '/search') setPage('search');
            else if (hash === '/settings') setPage('settings');
            else if (hash === '/import') setPage('import');
            else setPage('brain');
        }
        window.addEventListener('hashchange', onHash);
        onHash();

        return () => {
            sse.disconnect();
            window.removeEventListener('hashchange', onHash);
        };
    }, [authState]);

    // Show loading spinner
    if (authState === 'loading') {
        return html`<div class="login-screen"><div class="login-card" style="text-align:center;"><p style="color:var(--text-muted, #6b7280);">Loading...</p></div></div>`;
    }

    // Show login screen
    if (authState === 'login') {
        return html`<${LoginScreen} onSuccess=${() => setAuthState('ready')} />`;
    }

    function navigate(p) {
        window.location.hash = p === 'brain' ? '/' : '/' + p;
    }

    const onSelectMemory = useCallback((node) => {
        setSelectedMemory(node);
    }, []);

    return html`
        <div class="sidebar">
            <div class="sidebar-logo">S</div>
            <button class="sidebar-btn ${page === 'brain' ? 'active' : ''}" onClick=${() => navigate('brain')} title="Cerebrum">
                ${icons.brain}
            </button>
            <button class="sidebar-btn ${page === 'search' ? 'active' : ''}" onClick=${() => navigate('search')} title="Search">
                ${icons.search}
            </button>
            <button class="sidebar-btn ${page === 'import' ? 'active' : ''}" onClick=${() => navigate('import')} title="Import">
                ${icons.import}
            </button>
            <button class="sidebar-btn ${page === 'settings' ? 'active' : ''}" onClick=${() => navigate('settings')} title="Settings">
                ${icons.settings}
            </button>
            <div style="flex:1;"></div>
            <button class="sidebar-btn" onClick=${() => setShowHelp(true)} title="Help">
                ${icons.help}
            </button>
        </div>
        <div class="main-content">
            <div class="top-bar">
                <h1>CEREBRUM <span style="font-size:12px;font-weight:400;color:var(--text-muted);margin-left:6px">Your SAGE Brain</span></h1>
                <div class="spacer"></div>
                <div class="connection-badge">
                    <div class="connection-dot ${sseConnected ? '' : 'disconnected'}"></div>
                    ${sseConnected ? 'Live' : 'Connecting...'}
                </div>
            </div>
            <${HealthBar} />

            ${page === 'brain' && html`
                <${BrainView} sse=${sseRef.current} onSelectMemory=${onSelectMemory} />
                <${TimelineBar} />
            `}
            ${page === 'search' && html`<${SearchPage} onSelectMemory=${onSelectMemory} />`}
            ${page === 'import' && html`<${ImportPage} />`}
            ${page === 'settings' && html`<${SettingsPage} />`}

            <${MemoryDetail}
                memory=${selectedMemory}
                onClose=${() => setSelectedMemory(null)}
                onDelete=${() => setSelectedMemory(null)}
            />
        </div>
        ${showHelp && html`<${HelpOverlay} onClose=${() => setShowHelp(false)} />`}
    `;
}

// Mount
render(html`<${App} />`, document.getElementById('app'));
