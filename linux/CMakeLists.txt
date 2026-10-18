cmake_minimum_required(VERSION 3.21)
project(KyDriveDesktop VERSION 0.1.0 LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)

if(CMAKE_CXX_COMPILER_ID MATCHES "GNU|Clang")
  add_compile_options(-Wall -Wextra -Wshadow -Wnon-virtual-dtor -Woverloaded-virtual)
  add_compile_options(-fstack-protector-strong -fno-common)
  add_compile_definitions(
    _GLIBCXX_ASSERTIONS
    $<$<OR:$<CONFIG:Release>,$<CONFIG:RelWithDebInfo>,$<CONFIG:MinSizeRel>>:_FORTIFY_SOURCE=3>)
  add_link_options(-Wl,-z,relro -Wl,-z,now -Wl,-z,noexecstack)
endif()

find_package(Qt6 6.5 REQUIRED COMPONENTS Core Gui Widgets Network Qml Quick QuickControls2 Test)
find_package(KF6Kirigami REQUIRED)
find_package(KF6I18n REQUIRED)
find_package(KF6StatusNotifierItem REQUIRED)
find_package(KF6DBusAddons REQUIRED)
include(GNUInstallDirs)

# CoreClient has no QML or KF6 dependency, so the tests link it alone.
add_library(coreclient STATIC src/CoreClient.cpp)
target_include_directories(coreclient PUBLIC src)
target_link_libraries(coreclient PUBLIC Qt6::Core Qt6::Gui Qt6::Network)

add_executable(kydrive-desktop src/main.cpp qml/qml.qrc)
target_link_libraries(kydrive-desktop PRIVATE coreclient Qt6::Widgets Qt6::Qml Qt6::Quick Qt6::QuickControls2
  KF6::Kirigami KF6::I18n KF6::I18nQml KF6::StatusNotifierItem KF6::DBusAddons)
target_compile_definitions(kydrive-desktop PRIVATE KYDRIVE_SYNC_PATH="${CMAKE_INSTALL_FULL_BINDIR}/kydrive-sync")

install(TARGETS kydrive-desktop RUNTIME DESTINATION ${CMAKE_INSTALL_BINDIR})
install(FILES packaging/com.kysecurity.drive.desktop DESTINATION ${CMAKE_INSTALL_DATADIR}/applications)
install(FILES packaging/com.kysecurity.drive.metainfo.xml DESTINATION ${CMAKE_INSTALL_DATADIR}/metainfo)
install(FILES packaging/icons/hicolor/256x256/apps/com.kysecurity.drive.png DESTINATION ${CMAKE_INSTALL_DATADIR}/icons/hicolor/256x256/apps)
configure_file(packaging/kydrive-sync.service.in kydrive-sync.service @ONLY)
install(FILES ${CMAKE_CURRENT_BINARY_DIR}/kydrive-sync.service DESTINATION ${CMAKE_INSTALL_PREFIX}/lib/systemd/user)

include(CTest)
if(BUILD_TESTING)
  add_executable(tst_coreclient tests/tst_coreclient.cpp)
  target_link_libraries(tst_coreclient PRIVATE coreclient Qt6::Test)
  add_test(NAME coreclient COMMAND tst_coreclient)
  add_executable(tst_qml tests/tst_qml.cpp qml/qml.qrc)
  target_link_libraries(tst_qml PRIVATE coreclient Qt6::Test Qt6::Widgets Qt6::Qml Qt6::Quick KF6::Kirigami KF6::I18nQml)
  add_test(NAME qml COMMAND tst_qml)
  set_tests_properties(qml PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
endif()
