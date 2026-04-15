import { NavLink } from 'react-router-dom'

function Sidebar() {
  return (
    <div className="sidebar">
      <div className="sidebar-header">
        <h1 className="sidebar-logo">Towel</h1>
        <p className="sidebar-subtitle">Gmail AI Manager</p>
      </div>
      <nav className="sidebar-nav">
        <NavLink to="/chat" className={({ isActive }) => isActive ? 'nav-link active' : 'nav-link'}>
          <div className="nav-icon">💬</div>
          <span>Chat</span>
        </NavLink>
        <NavLink to="/history" className={({ isActive }) => isActive ? 'nav-link active' : 'nav-link'}>
          <div className="nav-icon">📋</div>
          <span>History</span>
        </NavLink>
      </nav>
    </div>
  )
}

export default Sidebar
